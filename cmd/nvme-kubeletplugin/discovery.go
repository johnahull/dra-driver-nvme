package main

import (
	"fmt"

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

func enumerateDevices() (AllocatableDevices, error) {
	nvmeDevices, err := nvme.Discover()
	if err != nil {
		return nil, fmt.Errorf("NVMe discovery failed: %w", err)
	}

	devices := make(AllocatableDevices)
	for i, dev := range nvmeDevices {
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

		name := fmt.Sprintf("nvme-%d", i)
		devices[name] = &AllocatableDevice{
			Info:         dev,
			pciBusIDAttr: pciBusIDAttr,
			pcieRootAttr: pcieRootAttr,
		}
		klog.Infof("Registered device %s: PCI=%s NUMA=%d model=%s",
			name, dev.PCIAddress, dev.NUMANode, dev.Model)
	}

	klog.Infof("Discovered %d NVMe devices", len(devices))
	return devices, nil
}
