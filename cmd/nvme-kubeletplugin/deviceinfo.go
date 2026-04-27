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
