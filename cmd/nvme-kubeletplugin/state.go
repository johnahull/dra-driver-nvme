package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/klog/v2"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	cdiVendor = "k8s.dra.nvme"
	cdiClass  = "nvme"
	cdiKind   = cdiVendor + "/" + cdiClass
)

type DeviceState struct {
	sync.Mutex
	allocatable AllocatableDevices
	prepared    map[string][]PreparedNvme // claimUID -> prepared devices
	cdiCache    *cdiapi.Cache
}

type PreparedNvme struct {
	drapbv1.Device
}

func NewDeviceState(f *flags) (*DeviceState, error) {
	allocatable, err := enumerateDevices()
	if err != nil {
		return nil, err
	}

	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(f.cdiRoot))
	if err != nil {
		return nil, fmt.Errorf("failed to create CDI cache: %w", err)
	}

	return &DeviceState{
		allocatable: allocatable,
		prepared:    make(map[string][]PreparedNvme),
		cdiCache:    cache,
	}, nil
}

func (s *DeviceState) Prepare(claim *resourceapi.ResourceClaim) ([]PreparedNvme, error) {
	s.Lock()
	defer s.Unlock()

	claimUID := string(claim.UID)

	// Return already-prepared devices
	if existing, ok := s.prepared[claimUID]; ok {
		return existing, nil
	}

	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim not yet allocated")
	}

	// Collect all CDI devices for this claim into one spec
	var cdiDevices []cdispec.Device
	var prepared []PreparedNvme

	for _, result := range claim.Status.Allocation.Devices.Results {
		// Skip devices from other drivers in multi-driver claims
		if result.Driver != DriverName {
			continue
		}

		allocDev, exists := s.allocatable[result.Device]
		if !exists {
			return nil, fmt.Errorf("NVMe device not found: %s", result.Device)
		}

		// Build CDI container edits with block device paths
		var deviceNodes []*cdispec.DeviceNode
		// Controller character device
		ctrlPath := fmt.Sprintf("/dev/%s", allocDev.Info.Controller)
		deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
			Path:     ctrlPath,
			HostPath: ctrlPath,
			Type:     "c",
		})
		// Namespace block devices
		for _, ns := range allocDev.Info.Namespaces {
			deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
				Path:     ns.DevicePath,
				HostPath: ns.DevicePath,
				Type:     "b",
			})
		}

		cdiDeviceName := fmt.Sprintf("%s-%s", claimUID, result.Device)
		cdiDevices = append(cdiDevices, cdispec.Device{
			Name: cdiDeviceName,
			ContainerEdits: cdispec.ContainerEdits{
				DeviceNodes: deviceNodes,
			},
		})

		cdiDeviceID := cdiparser.QualifiedName(cdiVendor, cdiClass, cdiDeviceName)
		prepared = append(prepared, PreparedNvme{
			Device: drapbv1.Device{
				RequestNames: []string{result.Request},
				PoolName:     result.Pool,
				DeviceName:   result.Device,
				CdiDeviceIds: []string{cdiDeviceID},
			},
		})

		klog.Infof("Prepared NVMe %s for claim %s: PCI=%s, namespaces=%d",
			result.Device, claimUID, allocDev.Info.PCIAddress, len(allocDev.Info.Namespaces))
	}

	// Write one CDI spec per claim containing all devices
	if len(cdiDevices) > 0 {
		specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
		spec := &cdispec.Spec{
			Kind:    cdiKind,
			Devices: cdiDevices,
		}
		minVersion, err := cdiapi.MinimumRequiredVersion(spec)
		if err != nil {
			return nil, fmt.Errorf("failed to get CDI spec version: %w", err)
		}
		spec.Version = minVersion

		if err := s.cdiCache.WriteSpec(spec, specName); err != nil {
			return nil, fmt.Errorf("failed to write CDI spec: %w", err)
		}
	}

	s.prepared[claimUID] = prepared
	return prepared, nil
}

func (s *DeviceState) Unprepare(claimUID string) error {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.prepared[claimUID]; !ok {
		return nil
	}

	// Remove CDI spec
	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
	if err := s.cdiCache.RemoveSpec(specName); err != nil {
		klog.Warningf("Failed to remove CDI spec for claim %s: %v", claimUID, err)
	}

	delete(s.prepared, claimUID)
	klog.Infof("Unprepared NVMe devices for claim %s", claimUID)
	return nil
}

// PrepareVFIO binds an NVMe device to vfio-pci for VM passthrough.
func (s *DeviceState) PrepareVFIO(pciAddr string) (string, error) {
	// Unbind from current driver
	driverPath := fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)
	if target, err := os.Readlink(driverPath); err == nil {
		currentDriver := filepath.Base(target)
		klog.Infof("Unbinding %s from %s", pciAddr, currentDriver)
		unbindPath := fmt.Sprintf("/sys/bus/pci/drivers/%s/unbind", currentDriver)
		if err := os.WriteFile(unbindPath, []byte(pciAddr), 0644); err != nil {
			return "", fmt.Errorf("failed to unbind %s: %w", pciAddr, err)
		}
	}

	// Set driver_override to vfio-pci
	overridePath := fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr)
	if err := os.WriteFile(overridePath, []byte("vfio-pci"), 0644); err != nil {
		return "", fmt.Errorf("failed to set driver_override: %w", err)
	}

	// Bind to vfio-pci
	bindPath := "/sys/bus/pci/drivers/vfio-pci/bind"
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0644); err != nil {
		return "", fmt.Errorf("failed to bind to vfio-pci: %w", err)
	}

	// Clear driver_override
	if err := os.WriteFile(overridePath, []byte(""), 0644); err != nil {
		klog.Warningf("Failed to clear driver_override for %s: %v", pciAddr, err)
	}

	// Get IOMMU group
	iommuLink := fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)
	target, err := os.Readlink(iommuLink)
	if err != nil {
		return "", fmt.Errorf("failed to read IOMMU group: %w", err)
	}
	iommuGroup := filepath.Base(target)

	klog.Infof("Bound %s to vfio-pci, IOMMU group %s", pciAddr, iommuGroup)
	return iommuGroup, nil
}

// UnprepareVFIO rebinds an NVMe device from vfio-pci back to the nvme driver.
func (s *DeviceState) UnprepareVFIO(pciAddr string) {
	unbindPath := "/sys/bus/pci/drivers/vfio-pci/unbind"
	if err := os.WriteFile(unbindPath, []byte(pciAddr), 0644); err != nil {
		klog.Warningf("Failed to unbind %s from vfio-pci: %v", pciAddr, err)
	}

	bindPath := "/sys/bus/pci/drivers/nvme/bind"
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0644); err != nil {
		klog.Warningf("Failed to rebind %s to nvme: %v", pciAddr, err)
	}
}
