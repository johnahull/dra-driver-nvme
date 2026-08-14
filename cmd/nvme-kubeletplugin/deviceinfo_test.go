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
	"testing"

	"github.com/johnahull/dra-driver-nvme/pkg/nvme"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func counterInt64(t *testing.T, c resourceapi.Counter) int64 {
	t.Helper()
	q := c.Value
	return (&q).Value()
}

func capInt64(t *testing.T, c resourceapi.DeviceCapacity) int64 {
	t.Helper()
	q := c.Value
	return (&q).Value()
}

func qtyInt64(q resource.Quantity) int64 {
	return (&q).Value()
}

func testController() nvme.DeviceInfo {
	return nvme.DeviceInfo{
		Controller:  "nvme0",
		PCIAddress:  "0000:3b:00.0",
		NUMANode:    0,
		CPUSocketID: 0,
		Model:       "Samsung_SSD_990",
		Serial:      "S123",
		FirmwareRev: "1.0",
		Transport:   "pcie",
		Namespaces: []nvme.NamespaceInfo{
			{Name: "nvme0n1", DevicePath: "/dev/nvme0n1", SizeBytes: 256 * 1024 * 1024 * 1024},
			{Name: "nvme0n2", DevicePath: "/dev/nvme0n2", SizeBytes: 512 * 1024 * 1024 * 1024},
		},
	}
}

func TestIsNamespaceDevice(t *testing.T) {
	ctrl := testController()

	d := &AllocatableDevice{Info: ctrl}
	if d.IsNamespaceDevice() {
		t.Error("controller device should not be a namespace device")
	}

	d2 := &AllocatableDevice{Info: ctrl, Namespace: &ctrl.Namespaces[0]}
	if !d2.IsNamespaceDevice() {
		t.Error("namespace device should be a namespace device")
	}
}

func TestGetSharedCounterSetName(t *testing.T) {
	d := &AllocatableDevice{Info: testController()}
	got := d.GetSharedCounterSetName()
	if got != "nvme0-counter-set" {
		t.Errorf("GetSharedCounterSetName() = %q, want %q", got, "nvme0-counter-set")
	}
}

func TestGetSharedCounterSet(t *testing.T) {
	t.Run("has capacity", func(t *testing.T) {
		d := &AllocatableDevice{Info: testController()}
		cs := d.GetSharedCounterSet()
		if cs == nil {
			t.Fatal("GetSharedCounterSet() returned nil")
		}
		if cs.Name != "nvme0-counter-set" {
			t.Errorf("counter set name = %q, want %q", cs.Name, "nvme0-counter-set")
		}
		counter, ok := cs.Counters[CapacityCounterName]
		if !ok {
			t.Fatalf("counter %q not found", CapacityCounterName)
		}
		expectedBytes := int64((256 + 512) * 1024 * 1024 * 1024)
		if qtyInt64(counter.Value) != expectedBytes {
			t.Errorf("capacity = %d, want %d", qtyInt64(counter.Value), expectedBytes)
		}
	})

	t.Run("zero capacity returns nil", func(t *testing.T) {
		d := &AllocatableDevice{Info: nvme.DeviceInfo{Controller: "nvme1"}}
		if cs := d.GetSharedCounterSet(); cs != nil {
			t.Errorf("expected nil for zero-capacity controller, got %v", cs)
		}
	})
}

func TestGetDeviceController(t *testing.T) {
	d := &AllocatableDevice{Info: testController()}
	dev := d.GetDevice("nvme0", ScalarNUMAAttr)

	if dev.Name != "nvme0" {
		t.Errorf("name = %q, want %q", dev.Name, "nvme0")
	}

	if len(dev.ConsumesCounters) != 1 {
		t.Fatalf("ConsumesCounters length = %d, want 1", len(dev.ConsumesCounters))
	}
	cc := dev.ConsumesCounters[0]
	if cc.CounterSet != "nvme0-counter-set" {
		t.Errorf("counter set = %q, want %q", cc.CounterSet, "nvme0-counter-set")
	}
	expectedBytes := int64((256 + 512) * 1024 * 1024 * 1024)
	if counterInt64(t, cc.Counters[CapacityCounterName]) != expectedBytes {
		t.Errorf("consumed capacity = %d, want %d", counterInt64(t, cc.Counters[CapacityCounterName]), expectedBytes)
	}

	cap, ok := dev.Capacity["dra.nvme/size"]
	if !ok {
		t.Fatal("capacity dra.nvme/size not found")
	}
	if capInt64(t, cap) != expectedBytes {
		t.Errorf("device capacity = %d, want %d", capInt64(t, cap), expectedBytes)
	}
}

func TestGetDeviceNamespace(t *testing.T) {
	ctrl := testController()
	d := &AllocatableDevice{Info: ctrl, Namespace: &ctrl.Namespaces[0]}
	dev := d.GetDevice("nvme0-nvme0n1", ScalarNUMAAttr)

	if dev.Name != "nvme0-nvme0n1" {
		t.Errorf("name = %q, want %q", dev.Name, "nvme0-nvme0n1")
	}

	nsNameAttr, ok := dev.Attributes["dra.nvme/namespaceName"]
	if !ok || nsNameAttr.StringValue == nil || *nsNameAttr.StringValue != "nvme0n1" {
		t.Errorf("namespaceName attribute missing or wrong")
	}

	ctrlNameAttr, ok := dev.Attributes["dra.nvme/controllerName"]
	if !ok || ctrlNameAttr.StringValue == nil || *ctrlNameAttr.StringValue != "nvme0" {
		t.Errorf("controllerName attribute missing or wrong")
	}

	if len(dev.ConsumesCounters) != 1 {
		t.Fatalf("ConsumesCounters length = %d, want 1", len(dev.ConsumesCounters))
	}
	cc := dev.ConsumesCounters[0]
	if cc.CounterSet != "nvme0-counter-set" {
		t.Errorf("counter set = %q, want %q", cc.CounterSet, "nvme0-counter-set")
	}
	nsBytes := int64(256 * 1024 * 1024 * 1024)
	if counterInt64(t, cc.Counters[CapacityCounterName]) != nsBytes {
		t.Errorf("consumed capacity = %d, want %d (namespace size)", counterInt64(t, cc.Counters[CapacityCounterName]), nsBytes)
	}

	cap, ok := dev.Capacity["dra.nvme/size"]
	if !ok {
		t.Fatal("capacity dra.nvme/size not found")
	}
	if capInt64(t, cap) != nsBytes {
		t.Errorf("device capacity = %d, want %d", capInt64(t, cap), nsBytes)
	}
}
