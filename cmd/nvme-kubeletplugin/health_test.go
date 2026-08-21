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
	}{
		{
			name: "live is healthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("live\n")
			},
			wantStatus: kubeletplugin.HealthStatusHealthy,
		},
		{
			name: "new is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("new")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
		},
		{
			name: "resetting is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("resetting")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
		},
		{
			name: "connecting is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("connecting")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
		},
		{
			name: "deleting is unhealthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("deleting")
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
		},
		{
			name: "deleting (no IO) is unhealthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("deleting (no IO)")
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
		},
		{
			name: "dead is unhealthy",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("dead")
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
		},
		{
			name: "unrecognized value is unknown",
			setup: func(m *mockSysfsOps) {
				m.files[statePath] = []byte("some-future-state")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
		},
		{
			name: "state missing, PCI present is unknown (VFIO or detached)",
			setup: func(m *mockSysfsOps) {
				m.fileErrs[statePath] = os.ErrNotExist
				m.stats[pciPath] = true
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
		},
		{
			name: "state missing, PCI absent is unhealthy (removed)",
			setup: func(m *mockSysfsOps) {
				m.fileErrs[statePath] = os.ErrNotExist
			},
			wantStatus: kubeletplugin.HealthStatusUnhealthy,
		},
		{
			name: "non-NotExist read error is unknown",
			setup: func(m *mockSysfsOps) {
				m.fileErrs[statePath] = errors.New("EIO")
			},
			wantStatus: kubeletplugin.HealthStatusUnknown,
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
