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
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

const CapacityCounterName = "capacity"

func (d *AllocatableDevice) IsNamespaceDevice() bool {
	return d.Namespace != nil
}

func (d *AllocatableDevice) totalBytes() int64 {
	var total int64
	for _, ns := range d.Info.Namespaces {
		total += int64(ns.SizeBytes)
	}
	return total
}

// GetSharedCounterSetName returns the KEP-4815 counter set name for this
// controller (e.g., "nvme0-counter-set").
func (d *AllocatableDevice) GetSharedCounterSetName() string {
	return fmt.Sprintf("%s-counter-set", d.Info.Controller)
}

// GetSharedCounterSet returns the KEP-4815 CounterSet for this controller.
// The single "capacity" counter represents total bytes across all namespaces.
func (d *AllocatableDevice) GetSharedCounterSet() *resourceapi.CounterSet {
	total := d.totalBytes()
	if total == 0 {
		return nil
	}
	return &resourceapi.CounterSet{
		Name: d.GetSharedCounterSetName(),
		Counters: map[string]resourceapi.Counter{
			CapacityCounterName: {Value: *resource.NewQuantity(total, resource.BinarySI)},
		},
	}
}

func (d *AllocatableDevice) controllerTopologyAttrs(attrForm NUMAAttrForm) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"dra.nvme/model":                     {StringValue: ptr.To(d.Info.Model)},
		"dra.nvme/serial":                    {StringValue: ptr.To(d.Info.Serial)},
		"dra.nvme/firmwareRev":               {StringValue: ptr.To(d.Info.FirmwareRev)},
		"dra.nvme/transport":                 {StringValue: ptr.To(d.Info.Transport)},
		"dra.nvme/numaNode":                  {IntValue: ptr.To(int64(d.Info.NUMANode))},
		"resource.kubernetes.io/cpuSocketID": {IntValue: ptr.To(int64(d.Info.CPUSocketID))},
	}
	numaAttr, err := getNUMANodeAttribute(d.Info.PCIAddress, attrForm)
	if err != nil {
		klog.Warningf("Failed to get numaNode attribute for %s: %v", d.Info.PCIAddress, err)
	} else {
		attrs[numaAttr.Name] = numaAttr.Value
	}
	if d.pciBusIDAttr.Name != "" {
		attrs[d.pciBusIDAttr.Name] = d.pciBusIDAttr.Value
	}
	if d.pcieRootAttr.Name != "" {
		attrs[d.pcieRootAttr.Name] = d.pcieRootAttr.Value
	}
	return attrs
}

// GetDevice returns the DRA Device representation for a ResourceSlice.
// For controller devices (Namespace == nil), includes full capacity and
// ConsumesCounters for the entire controller.
// For namespace devices (Namespace != nil), includes namespace-specific
// capacity and ConsumesCounters for that namespace's share.
func (d *AllocatableDevice) GetDevice(name string, attrForm NUMAAttrForm) resourceapi.Device {
	if d.IsNamespaceDevice() {
		return d.getNamespaceDevice(name, attrForm)
	}
	return d.getControllerDevice(name, attrForm)
}

func (d *AllocatableDevice) getControllerDevice(name string, attrForm NUMAAttrForm) resourceapi.Device {
	attrs := d.controllerTopologyAttrs(attrForm)
	totalBytes := d.totalBytes()

	dev := resourceapi.Device{
		Name:       name,
		Attributes: attrs,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"dra.nvme/size": {Value: *resource.NewQuantity(totalBytes, resource.BinarySI)},
		},
	}

	csName := d.GetSharedCounterSetName()
	if totalBytes > 0 {
		dev.ConsumesCounters = []resourceapi.DeviceCounterConsumption{{
			CounterSet: csName,
			Counters: map[string]resourceapi.Counter{
				CapacityCounterName: {Value: *resource.NewQuantity(totalBytes, resource.BinarySI)},
			},
		}}
	}

	return dev
}

func (d *AllocatableDevice) getNamespaceDevice(name string, attrForm NUMAAttrForm) resourceapi.Device {
	attrs := d.controllerTopologyAttrs(attrForm)
	attrs["dra.nvme/namespaceName"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.Namespace.Name)}
	attrs["dra.nvme/controllerName"] = resourceapi.DeviceAttribute{StringValue: ptr.To(d.Info.Controller)}

	nsBytes := int64(d.Namespace.SizeBytes)

	dev := resourceapi.Device{
		Name:       name,
		Attributes: attrs,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"dra.nvme/size": {Value: *resource.NewQuantity(nsBytes, resource.BinarySI)},
		},
	}

	csName := d.GetSharedCounterSetName()
	if nsBytes > 0 {
		dev.ConsumesCounters = []resourceapi.DeviceCounterConsumption{{
			CounterSet: csName,
			Counters: map[string]resourceapi.Counter{
				CapacityCounterName: {Value: *resource.NewQuantity(nsBytes, resource.BinarySI)},
			},
		}}
	}

	return dev
}
