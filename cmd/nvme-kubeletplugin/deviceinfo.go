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
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

// GetDevice returns the DRA Device representation for a ResourceSlice.
func (d *AllocatableDevice) GetDevice(name string) resourceapi.Device {
	attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"dra.nvme/model":       {StringValue: ptr.To(d.Info.Model)},
		"dra.nvme/serial":      {StringValue: ptr.To(d.Info.Serial)},
		"dra.nvme/firmwareRev": {StringValue: ptr.To(d.Info.FirmwareRev)},
		"dra.nvme/transport":   {StringValue: ptr.To(d.Info.Transport)},
		// Vendor-specific NUMA
		"dra.nvme/numaNode": {IntValue: ptr.To(int64(d.Info.NUMANode))},
		// Standardized topology attributes for cross-driver matchAttribute
		"resource.kubernetes.io/numaNode":    {IntValue: ptr.To(int64(d.Info.NUMANode))},
		"resource.kubernetes.io/cpuSocketID": {IntValue: ptr.To(int64(d.Info.CPUSocketID))},
	}

	// Standard PCI topology attributes from deviceattribute library
	if d.pciBusIDAttr.Name != "" {
		attrs[d.pciBusIDAttr.Name] = d.pciBusIDAttr.Value
	}
	if d.pcieRootAttr.Name != "" {
		attrs[d.pcieRootAttr.Name] = d.pcieRootAttr.Value
	}

	// Total size across all namespaces
	var totalBytes int64
	for _, ns := range d.Info.Namespaces {
		totalBytes += int64(ns.SizeBytes)
	}

	return resourceapi.Device{
		Name:       name,
		Attributes: attrs,
		Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"dra.nvme/size": {Value: *resource.NewQuantity(totalBytes, resource.BinarySI)},
		},
	}
}
