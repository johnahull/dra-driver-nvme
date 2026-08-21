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
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/johnahull/dra-driver-nvme/pkg/nvme"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

func TestControllerHealth(t *testing.T) {
	const ctrl = "nvme0"
	const pciAddr = "0000:3b:00.0"
	statePath := fmt.Sprintf(nvmeStatePathFmt, ctrl)
	pciPath := fmt.Sprintf(pciDevicePathFmt, pciAddr)

	tests := []struct {
		name       string
		setup      func(*mockSysfsOps)
		wantStatus kubeletplugin.HealthStatus
		wantMsg    string
	}{
		{
			name: "live is healthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("live\n")
			},
			wantStatus: kubeletplugin.HealthStatusHealthy,
			wantMsg:    "",
		},
		{
			name: "new is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("new")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
			wantMsg:    "new",
		},
		{
			name: "resetting is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("resetting")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
			wantMsg:    "resetting",
		},
		{
			name: "connecting is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("connecting")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
			wantMsg:    "connecting",
		},
		{
			name: "deleting is unhealthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("deleting")
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
			wantMsg:    "deleting",
		},
		{
			name: "deleting (no IO) is unhealthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("deleting (no IO)")
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
			wantMsg:    "deleting (no IO)",
		},
		{
			name: "dead is unhealthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("dead")
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
			wantMsg:    "dead",
		},
		{
			name: "unrecognized value is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("some-future-state")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
			wantMsg:    "some-future-state",
		},
		{
			name: "state missing, PCI present is unknown (VFIO or detached)",
			setup: func(m *mockSysfsOps) {
				m.fileErrs[statePath] = os.ErrNotExist
				m.stats[pciPath] = true
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
			wantMsg:    "not bound to nvme driver (VFIO passthrough or driver detached)",
		},
		{
			name: "state missing, PCI absent is unhealthy (removed)",
			setup: func(m *mockSysfsOps) {
				m.fileErrs[statePath] = os.ErrNotExist
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
			wantMsg:    "device removed",
		},
		{
			name: "non-NotExist read error is unknown",
			setup: func(m *mockSysfsOps) {
				m.fileErrs[statePath] = errors.New("EIO")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
			wantMsg:    "failed to read controller state: EIO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockSysfs()
			tt.setup(m)
			withMockSysfs(t, m)

			status, msg := controllerHealth(ctrl, pciAddr)
			if status != tt.wantStatus {
				t.Errorf("controllerHealth() status = %v, want %v (msg: %q)", status, tt.wantStatus, msg)
			}
			if msg != tt.wantMsg {
				t.Errorf("controllerHealth() msg = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}

func TestBuildHealthReport(t *testing.T) {
	ctrl := testController()

	m := newMockSysfs()
	m.files[fmt.Sprintf(nvmeStatePathFmt, ctrl.Controller)] = []byte("live")
	withMockSysfs(t, m)

	allocatable := AllocatableDevices{
		ctrl.Controller: {Info: ctrl},
	}
	for i := range ctrl.Namespaces {
		name := fmt.Sprintf("%s-%s", ctrl.Controller, ctrl.Namespaces[i].Name)
		allocatable[name] = &AllocatableDevice{Info: ctrl, Namespace: &ctrl.Namespaces[i]}
	}

	d := &driver{
		state:    &DeviceState{allocatable: allocatable},
		nodeName: "test-node",
	}

	report := d.buildHealthReport()

	if len(report.Devices) != len(allocatable) {
		t.Fatalf("got %d device health entries, want %d", len(report.Devices), len(allocatable))
	}

	seen := make(map[string]kubeletplugin.DeviceHealth)
	for _, dh := range report.Devices {
		seen[dh.DeviceName] = dh
	}

	for name := range allocatable {
		dh, ok := seen[name]
		if !ok {
			t.Errorf("missing DeviceHealth for %q", name)
			continue
		}
		if dh.PoolName != "test-node" {
			t.Errorf("device %q: PoolName = %q, want %q", name, dh.PoolName, "test-node")
		}
		if dh.Health != kubeletplugin.HealthStatusHealthy {
			t.Errorf("device %q: Health = %v, want %v", name, dh.Health, kubeletplugin.HealthStatusHealthy)
		}
		if dh.LastUpdated.IsZero() {
			t.Errorf("device %q: LastUpdated is zero", name)
		}
	}
}

// countingSysfsOps wraps mockSysfsOps to count ReadFile calls per path, used
// to verify buildHealthReport dedups sysfs reads per physical controller.
type countingSysfsOps struct {
	*mockSysfsOps
	readFileCalls map[string]int
}

func (c *countingSysfsOps) ReadFile(path string) ([]byte, error) {
	c.readFileCalls[path]++
	return c.mockSysfsOps.ReadFile(path)
}

func TestBuildHealthReportMultiController(t *testing.T) {
	live := testController()
	live.Controller = "nvme0"
	live.PCIAddress = "0000:3b:00.0"

	dead := testController()
	dead.Controller = "nvme1"
	dead.PCIAddress = "0000:3c:00.0"
	dead.Namespaces = []nvme.NamespaceInfo{{Name: "nvme1n1", DevicePath: "/dev/nvme1n1", SizeBytes: 1024}}

	m := &countingSysfsOps{mockSysfsOps: newMockSysfs(), readFileCalls: make(map[string]int)}
	m.files[fmt.Sprintf(nvmeStatePathFmt, live.Controller)] = []byte("live")
	m.files[fmt.Sprintf(nvmeStatePathFmt, dead.Controller)] = []byte("dead")
	withMockSysfs(t, m.mockSysfsOps)
	sysfs = m
	t.Cleanup(func() { sysfs = m.mockSysfsOps })

	allocatable := AllocatableDevices{
		live.Controller: {Info: live},
		dead.Controller: {Info: dead},
	}
	for i := range live.Namespaces {
		name := fmt.Sprintf("%s-%s", live.Controller, live.Namespaces[i].Name)
		allocatable[name] = &AllocatableDevice{Info: live, Namespace: &live.Namespaces[i]}
	}
	for i := range dead.Namespaces {
		name := fmt.Sprintf("%s-%s", dead.Controller, dead.Namespaces[i].Name)
		allocatable[name] = &AllocatableDevice{Info: dead, Namespace: &dead.Namespaces[i]}
	}

	d := &driver{state: &DeviceState{allocatable: allocatable}, nodeName: "test-node"}
	report := d.buildHealthReport()

	byName := make(map[string]kubeletplugin.DeviceHealth)
	for _, dh := range report.Devices {
		byName[dh.DeviceName] = dh
	}

	for name, dev := range allocatable {
		wantStatus := kubeletplugin.HealthStatusHealthy
		if dev.Info.Controller == dead.Controller {
			wantStatus = kubeletplugin.HealthStatusUnhealthy
		}
		dh, ok := byName[name]
		if !ok {
			t.Errorf("missing DeviceHealth for %q", name)
			continue
		}
		if dh.Health != wantStatus {
			t.Errorf("device %q: Health = %v, want %v", name, dh.Health, wantStatus)
		}
	}

	for _, ctrlName := range []string{live.Controller, dead.Controller} {
		path := fmt.Sprintf(nvmeStatePathFmt, ctrlName)
		if got := m.readFileCalls[path]; got != 1 {
			t.Errorf("ReadFile(%q) called %d times, want 1 (verdict should be cached per controller)", path, got)
		}
	}
}

func TestWatchHealthStatus(t *testing.T) {
	ctrl := testController()

	m := newMockSysfs()
	m.files[fmt.Sprintf(nvmeStatePathFmt, ctrl.Controller)] = []byte("live")
	withMockSysfs(t, m)

	d := &driver{
		state:    &DeviceState{allocatable: AllocatableDevices{ctrl.Controller: {Info: ctrl}}},
		nodeName: "test-node",
	}

	ctx, cancel := context.WithCancel(t.Context())
	reports := make(chan kubeletplugin.DeviceHealthReport)
	errCh := make(chan error, 1)
	go func() { errCh <- d.WatchHealthStatus(ctx, reports) }()

	select {
	case report := <-reports:
		if len(report.Devices) != 1 {
			t.Errorf("initial report has %d devices, want 1", len(report.Devices))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial health report")
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("WatchHealthStatus returned %v, want nil after ctx cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WatchHealthStatus to return after ctx cancel")
	}
}

func TestWatchHealthStatusPeriodicResend(t *testing.T) {
	ctrl := testController()

	m := newMockSysfs()
	m.files[fmt.Sprintf(nvmeStatePathFmt, ctrl.Controller)] = []byte("live")
	withMockSysfs(t, m)

	oldInterval := healthPollInterval
	healthPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { healthPollInterval = oldInterval })

	d := &driver{
		state:    &DeviceState{allocatable: AllocatableDevices{ctrl.Controller: {Info: ctrl}}},
		nodeName: "test-node",
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reports := make(chan kubeletplugin.DeviceHealthReport)
	go func() { _ = d.WatchHealthStatus(ctx, reports) }()

	for i := 0; i < 2; i++ {
		select {
		case <-reports:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for report %d", i+1)
		}
	}
}

func TestHandleError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCancel bool
	}{
		{
			name:       "fatal error cancels",
			err:        errors.New("boom"),
			wantCancel: true,
		},
		{
			name:       "health-recoverable error does not cancel",
			err:        fmt.Errorf("stale health report: %w", kubeletplugin.ErrRecoverable),
			wantCancel: false,
		},
		{
			name:       "publish-recoverable error does not cancel",
			err:        fmt.Errorf("failed to publish resourceslice: %w", kubeletplugin.ErrRecoverable),
			wantCancel: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canceled := false
			d := &driver{cancelCtx: func() { canceled = true }}
			d.HandleError(t.Context(), tt.err, "test error")
			if canceled != tt.wantCancel {
				t.Errorf("cancelCtx called = %v, want %v", canceled, tt.wantCancel)
			}
		})
	}
}
