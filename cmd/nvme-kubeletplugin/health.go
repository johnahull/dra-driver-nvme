// Copyright 2026 Red Hat, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

// healthPollInterval must stay below the kubelet's default 30s health-report
// lease (kubeletplugin.DeviceHealth.HealthCheckTimeout, zero value) so health
// never decays to Unknown between polls. Var (not const) so tests can shrink
// it to exercise the periodic re-send path.
var healthPollInterval = 15 * time.Second

const (
	nvmeStatePathFmt = "/sys/class/nvme/%s/state"
	pciDevicePathFmt = "/sys/bus/pci/devices/%s"
)

// controllerHealth derives a device's health from its NVMe controller state in
// sysfs. See the kernel's nvme_ctrl_state_name for the state vocabulary: new,
// live, resetting, connecting, deleting, "deleting (no IO)", dead.
func controllerHealth(controller, pciAddr string) (kubeletplugin.HealthStatus, string) {
	data, err := sysfs.ReadFile(fmt.Sprintf(nvmeStatePathFmt, controller))
	if err != nil {
		if os.IsNotExist(err) {
			// The controller is not bound to the nvme driver — either it was
			// rebound to vfio-pci for passthrough, or it faulted off the bus
			// entirely. Distinguish by checking physical PCI presence.
			if _, statErr := sysfs.Stat(fmt.Sprintf(pciDevicePathFmt, pciAddr)); statErr == nil {
				return kubeletplugin.HealthStatusUnknown, "not bound to nvme driver (VFIO passthrough or driver detached)"
			}
			return kubeletplugin.HealthStatusUnhealthy, "device removed"
		}
		// A non-NotExist read error (e.g. EIO) is ambiguous — report Unknown
		// rather than guessing the device has failed.
		return kubeletplugin.HealthStatusUnknown, fmt.Sprintf("failed to read controller state: %v", err)
	}

	state := strings.TrimSpace(string(data))
	switch state {
	case "live":
		return kubeletplugin.HealthStatusHealthy, ""
	case "new", "resetting", "connecting":
		return kubeletplugin.HealthStatusUnknown, state
	case "deleting", "deleting (no IO)", "dead":
		return kubeletplugin.HealthStatusUnhealthy, state
	default:
		return kubeletplugin.HealthStatusUnknown, state
	}
}

// buildHealthReport computes health for every allocatable device. Multiple
// allocatable entries (a controller device plus its namespace devices) can
// share one physical controller, so the verdict is computed once per
// controller and reused across all of that controller's entries.
//
// d.state.allocatable is populated once during driver startup and never
// mutated afterward (see DeviceState.allocatable doc comment), so it is safe
// to range over here without holding d.state.mu.
func (d *driver) buildHealthReport() kubeletplugin.DeviceHealthReport {
	now := time.Now()
	type verdict struct {
		status  kubeletplugin.HealthStatus
		message string
	}
	verdicts := make(map[string]verdict)

	report := kubeletplugin.DeviceHealthReport{
		Devices: make([]kubeletplugin.DeviceHealth, 0, len(d.state.allocatable)),
	}

	for _, name := range d.state.allocatable.SortedNames() {
		dev := d.state.allocatable[name]
		ctrl := dev.Info.Controller

		v, ok := verdicts[ctrl]
		if !ok {
			status, msg := controllerHealth(ctrl, dev.Info.PCIAddress)
			v = verdict{status: status, message: msg}
			verdicts[ctrl] = v
		}

		report.Devices = append(report.Devices, kubeletplugin.DeviceHealth{
			PoolName:    d.nodeName,
			DeviceName:  name,
			Health:      v.status,
			LastUpdated: now,
			Message:     v.message,
		})
	}

	return report
}

// WatchHealthStatus streams NVMe controller health to the kubelet. It sends an
// initial full report immediately, then re-sends on a fixed interval to keep
// the kubelet's health lease fresh until ctx is canceled. The method is
// stateless and safe to call again if the kubelet reconnects.
func (d *driver) WatchHealthStatus(ctx context.Context, reports chan<- kubeletplugin.DeviceHealthReport) error {
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()

	send := func() bool {
		report := d.buildHealthReport()
		select {
		case reports <- report:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if !send() {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !send() {
				return nil
			}
		}
	}
}
