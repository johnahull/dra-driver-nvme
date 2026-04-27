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
		"dra.nvme/numaNode":    {IntValue: ptr.To(int64(d.Info.NUMANode))},
	}

	// Standard topology attributes
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
