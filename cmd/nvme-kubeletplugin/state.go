package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	nvmeapi "github.com/johnahull/dra-driver-nvme/api"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	IsVFIO     bool
	PCIAddress string
}

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
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

	// Parse opaque configs to determine mode (block vs vfio)
	configs, err := getOpaqueDeviceConfigs(claim.Status.Allocation.Devices.Config)
	if err != nil {
		return nil, fmt.Errorf("error getting device configs: %w", err)
	}

	// Add default config at lowest precedence
	configs = slices.Insert(configs, 0, &OpaqueDeviceConfig{
		Requests: []string{},
		Config:   nvmeapi.DefaultNvmeConfig(),
	})

	// Map each allocation result to its config
	type deviceWithConfig struct {
		result *resourceapi.DeviceRequestAllocationResult
		config *nvmeapi.NvmeConfig
	}
	var devicesWithConfig []deviceWithConfig

	for _, result := range claim.Status.Allocation.Devices.Results {
		// Skip devices from other drivers in multi-driver claims
		if result.Driver != DriverName {
			continue
		}
		if _, exists := s.allocatable[result.Device]; !exists {
			return nil, fmt.Errorf("NVMe device not found: %s", result.Device)
		}

		// Find matching config (last match wins)
		var matchedConfig *nvmeapi.NvmeConfig
		for _, c := range slices.Backward(configs) {
			if len(c.Requests) == 0 || slices.Contains(c.Requests, result.Request) {
				cfg, ok := c.Config.(*nvmeapi.NvmeConfig)
				if ok {
					matchedConfig = cfg
					break
				}
			}
		}
		if matchedConfig == nil {
			matchedConfig = nvmeapi.DefaultNvmeConfig()
		}

		if err := matchedConfig.Normalize(); err != nil {
			return nil, fmt.Errorf("error normalizing config for %s: %w", result.Device, err)
		}
		if err := matchedConfig.Validate(); err != nil {
			return nil, fmt.Errorf("error validating config for %s: %w", result.Device, err)
		}

		resultCopy := result
		devicesWithConfig = append(devicesWithConfig, deviceWithConfig{
			result: &resultCopy,
			config: matchedConfig,
		})
	}

	// Prepare each device according to its config.
	// Track VFIO-bound devices for rollback on partial failure.
	var cdiDevices []cdispec.Device
	var prepared []PreparedNvme
	var vfioBound []string // PCI addresses bound to vfio-pci so far

	for _, dwc := range devicesWithConfig {
		result := dwc.result
		allocDev := s.allocatable[result.Device]

		var deviceNodes []*cdispec.DeviceNode
		isVFIO := dwc.config.Mode == "vfio"

		if isVFIO {
			// VFIO mode: bind to vfio-pci
			iommuGroup, err := s.prepareVFIO(allocDev.Info.PCIAddress)
			if err != nil {
				// Rollback any VFIO bindings done so far
				for _, addr := range vfioBound {
					s.unprepareVFIO(addr)
				}
				return nil, fmt.Errorf("VFIO prepare failed for %s: %w", result.Device, err)
			}
			vfioBound = append(vfioBound, allocDev.Info.PCIAddress)

			vfioDevPath := fmt.Sprintf("/dev/vfio/%s", iommuGroup)
			deviceNodes = []*cdispec.DeviceNode{
				{Path: "/dev/vfio/vfio", HostPath: "/dev/vfio/vfio", Type: "c"},
				{Path: vfioDevPath, HostPath: vfioDevPath, Type: "c"},
			}

			klog.Infof("Prepared NVMe %s (VFIO) for claim %s: PCI=%s IOMMU=%s",
				result.Device, claimUID, allocDev.Info.PCIAddress, iommuGroup)
		} else {
			// Block mode: expose /dev/nvme* devices
			ctrlPath := fmt.Sprintf("/dev/%s", allocDev.Info.Controller)
			deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
				Path: ctrlPath, HostPath: ctrlPath, Type: "c",
			})
			for _, ns := range allocDev.Info.Namespaces {
				deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
					Path: ns.DevicePath, HostPath: ns.DevicePath, Type: "b",
				})
			}

			klog.Infof("Prepared NVMe %s (block) for claim %s: PCI=%s namespaces=%d",
				result.Device, claimUID, allocDev.Info.PCIAddress, len(allocDev.Info.Namespaces))
		}

		cdiDeviceName := fmt.Sprintf("%s-%s", claimUID, result.Device)
		cdiDevices = append(cdiDevices, cdispec.Device{
			Name:           cdiDeviceName,
			ContainerEdits: cdispec.ContainerEdits{DeviceNodes: deviceNodes},
		})

		cdiDeviceID := cdiparser.QualifiedName(cdiVendor, cdiClass, cdiDeviceName)
		prepared = append(prepared, PreparedNvme{
			Device: drapbv1.Device{
				RequestNames: []string{result.Request},
				PoolName:     result.Pool,
				DeviceName:   result.Device,
				CdiDeviceIds: []string{cdiDeviceID},
			},
			IsVFIO:     isVFIO,
			PCIAddress: allocDev.Info.PCIAddress,
		})
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
			for _, addr := range vfioBound {
				s.unprepareVFIO(addr)
			}
			return nil, fmt.Errorf("failed to get CDI spec version: %w", err)
		}
		spec.Version = minVersion

		if err := s.cdiCache.WriteSpec(spec, specName); err != nil {
			for _, addr := range vfioBound {
				s.unprepareVFIO(addr)
			}
			return nil, fmt.Errorf("failed to write CDI spec: %w", err)
		}
	}

	s.prepared[claimUID] = prepared
	return prepared, nil
}

func (s *DeviceState) Unprepare(claimUID string) error {
	s.Lock()
	defer s.Unlock()

	devices, ok := s.prepared[claimUID]
	if !ok {
		return nil
	}

	// Rebind any VFIO devices back to nvme
	for _, dev := range devices {
		if dev.IsVFIO && dev.PCIAddress != "" {
			s.unprepareVFIO(dev.PCIAddress)
		}
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

// prepareVFIO binds an NVMe device to vfio-pci and returns the IOMMU group.
func (s *DeviceState) prepareVFIO(pciAddr string) (string, error) {
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

// unprepareVFIO rebinds an NVMe device from vfio-pci back to the nvme driver.
func (s *DeviceState) unprepareVFIO(pciAddr string) {
	unbindPath := "/sys/bus/pci/drivers/vfio-pci/unbind"
	if err := os.WriteFile(unbindPath, []byte(pciAddr), 0644); err != nil {
		klog.Warningf("Failed to unbind %s from vfio-pci: %v", pciAddr, err)
	}

	bindPath := "/sys/bus/pci/drivers/nvme/bind"
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0644); err != nil {
		klog.Warningf("Failed to rebind %s to nvme: %v", pciAddr, err)
	}
}

// getOpaqueDeviceConfigs extracts NvmeConfig objects from the claim's allocation configs.
// Returns configs in order of precedence (lowest first): class configs, then claim configs.
func getOpaqueDeviceConfigs(configs []resourceapi.DeviceAllocationConfiguration) ([]*OpaqueDeviceConfig, error) {
	var classConfigs, claimConfigs []*OpaqueDeviceConfig
	for _, config := range configs {
		if config.DeviceConfiguration.Opaque == nil {
			continue
		}
		if config.DeviceConfiguration.Opaque.Driver != DriverName {
			continue
		}
		decoded, err := runtime.Decode(nvmeapi.Decoder, config.DeviceConfiguration.Opaque.Parameters.Raw)
		if err != nil {
			return nil, fmt.Errorf("error decoding NvmeConfig: %w", err)
		}
		odc := &OpaqueDeviceConfig{
			Requests: config.Requests,
			Config:   decoded,
		}
		switch config.Source {
		case resourceapi.AllocationConfigSourceClass:
			classConfigs = append(classConfigs, odc)
		case resourceapi.AllocationConfigSourceClaim:
			claimConfigs = append(claimConfigs, odc)
		}
	}
	// Class configs first (lowest precedence), then claim configs (highest)
	return append(classConfigs, claimConfigs...), nil
}
