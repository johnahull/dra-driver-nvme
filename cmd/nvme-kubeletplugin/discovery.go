package main

import (
	"fmt"
	"sort"

	"github.com/johnahull/dra-driver-nvme/pkg/nvme"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/klog/v2"
)

// AllocatableDevice wraps an NVMe device with its topology attributes.
type AllocatableDevice struct {
	Info         nvme.DeviceInfo
	pciBusIDAttr deviceattribute.DeviceAttribute
	pcieRootAttr deviceattribute.DeviceAttribute
}

// AllocatableDevices maps canonical device names to their allocatable device info.
type AllocatableDevices map[string]*AllocatableDevice

// SortedNames returns device names in sorted order for deterministic ResourceSlice publication.
func (d AllocatableDevices) SortedNames() []string {
	names := make([]string, 0, len(d))
	for name := range d {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func enumerateDevices() (AllocatableDevices, error) {
	nvmeDevices, err := nvme.Discover()
	if err != nil {
		return nil, fmt.Errorf("NVMe discovery failed: %w", err)
	}

	devices := make(AllocatableDevices)
	for _, dev := range nvmeDevices {
		pciBusIDAttr, err := deviceattribute.GetPCIBusIDAttribute(dev.PCIAddress)
		if err != nil {
			klog.Warningf("Failed to get PCI Bus ID attribute for %s: %v", dev.Controller, err)
			continue
		}

		pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(dev.PCIAddress)
		if err != nil {
			klog.Warningf("Failed to get PCIe root attribute for %s: %v", dev.Controller, err)
			continue
		}

		// Use controller name as device name for stable identity across restarts
		name := dev.Controller
		devices[name] = &AllocatableDevice{
			Info:         dev,
			pciBusIDAttr: pciBusIDAttr,
			pcieRootAttr: pcieRootAttr,
		}
		klog.Infof("Registered device %s: PCI=%s NUMA=%d socket=%d model=%s",
			name, dev.PCIAddress, dev.NUMANode, dev.CPUSocketID, dev.Model)
	}

	klog.Infof("Discovered %d NVMe devices", len(devices))
	return devices, nil
}
