package main

import (
	"fmt"
	"sort"

	"github.com/johnahull/dra-driver-nvme/pkg/nvme"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/klog/v2"
)

type AllocatableDevice struct {
	Info         nvme.DeviceInfo
	pciBusIDAttr deviceattribute.DeviceAttribute
	pcieRootAttr deviceattribute.DeviceAttribute
}

type AllocatableDevices map[string]*AllocatableDevice

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
			klog.V(2).InfoS("Skipping device: failed to get PCI Bus ID attribute",
				"controller", dev.Controller, "err", err)
			continue
		}

		pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(dev.PCIAddress)
		if err != nil {
			klog.V(2).InfoS("Skipping device: failed to get PCIe root attribute",
				"controller", dev.Controller, "err", err)
			continue
		}

		name := dev.Controller
		devices[name] = &AllocatableDevice{
			Info:         dev,
			pciBusIDAttr: pciBusIDAttr,
			pcieRootAttr: pcieRootAttr,
		}
		klog.InfoS("Registered device",
			"name", name, "pci", dev.PCIAddress,
			"numa", dev.NUMANode, "socket", dev.CPUSocketID, "model", dev.Model)
	}

	klog.InfoS("NVMe discovery complete", "devices", len(devices))
	return devices, nil
}
