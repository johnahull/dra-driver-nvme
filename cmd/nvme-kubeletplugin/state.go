package main

import (
	"context"
	"encoding/json"
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
	cdiVendor      = "k8s.dra.nvme"
	cdiClass       = "nvme"
	cdiKind        = cdiVendor + "/" + cdiClass
	checkpointFile = "prepared-claims.json"
)

type DeviceState struct {
	mu             sync.Mutex
	allocatable    AllocatableDevices // immutable after initialization
	prepared       map[string][]PreparedNvme
	cdiCache       *cdiapi.Cache
	checkpointPath string
}

type PreparedNvme struct {
	drapbv1.Device
	IsVFIO     bool   `json:"isVFIO"`
	PCIAddress string `json:"pciAddress"`
}

type OpaqueDeviceConfig struct {
	Requests []string
	Config   runtime.Object
}

func NewDeviceState(ctx context.Context, f *flags) (*DeviceState, error) {
	logger := klog.FromContext(ctx)

	allocatable, err := enumerateDevices()
	if err != nil {
		return nil, err
	}

	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(f.cdiRoot))
	if err != nil {
		return nil, fmt.Errorf("failed to create CDI cache: %w", err)
	}

	checkpointPath := filepath.Join(f.pluginDataDirectoryPath, checkpointFile)

	s := &DeviceState{
		allocatable:    allocatable,
		prepared:       make(map[string][]PreparedNvme),
		cdiCache:       cache,
		checkpointPath: checkpointPath,
	}

	if err := s.restoreCheckpoint(logger); err != nil {
		logger.Error(err, "Failed to restore checkpoint, starting fresh")
	}

	return s, nil
}

func (s *DeviceState) Prepare(ctx context.Context, claim *resourceapi.ResourceClaim) ([]PreparedNvme, error) {
	logger := klog.FromContext(ctx)
	claimUID := string(claim.UID)

	s.mu.Lock()
	if existing, ok := s.prepared[claimUID]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.mu.Unlock()

	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim not yet allocated")
	}

	configs, err := getOpaqueDeviceConfigs(claim.Status.Allocation.Devices.Config)
	if err != nil {
		return nil, fmt.Errorf("error getting device configs: %w", err)
	}

	configs = slices.Insert(configs, 0, &OpaqueDeviceConfig{
		Requests: []string{},
		Config:   nvmeapi.DefaultNvmeConfig(),
	})

	type deviceWithConfig struct {
		result *resourceapi.DeviceRequestAllocationResult
		config *nvmeapi.NvmeConfig
	}

	s.mu.Lock()
	var devicesWithConfig []deviceWithConfig
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != DriverName {
			continue
		}
		if _, exists := s.allocatable[result.Device]; !exists {
			s.mu.Unlock()
			return nil, fmt.Errorf("NVMe device not found: %s", result.Device)
		}

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
			s.mu.Unlock()
			return nil, fmt.Errorf("error normalizing config for %s: %w", result.Device, err)
		}
		if err := matchedConfig.Validate(); err != nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("error validating config for %s: %w", result.Device, err)
		}

		resultCopy := result
		devicesWithConfig = append(devicesWithConfig, deviceWithConfig{
			result: &resultCopy,
			config: matchedConfig,
		})
	}
	s.mu.Unlock()

	var cdiDevices []cdispec.Device
	var prepared []PreparedNvme
	var vfioBound []string

	for _, dwc := range devicesWithConfig {
		result := dwc.result

		s.mu.Lock()
		allocDev := s.allocatable[result.Device]
		s.mu.Unlock()

		var deviceNodes []*cdispec.DeviceNode
		isVFIO := dwc.config.Mode == "vfio"

		if isVFIO {
			iommuGroup, err := prepareVFIO(allocDev.Info.PCIAddress)
			if err != nil {
				for _, addr := range vfioBound {
					unprepareVFIO(addr)
				}
				return nil, fmt.Errorf("VFIO prepare failed for %s: %w", result.Device, err)
			}
			vfioBound = append(vfioBound, allocDev.Info.PCIAddress)

			vfioDevPath := fmt.Sprintf("/dev/vfio/%s", iommuGroup)
			deviceNodes = []*cdispec.DeviceNode{
				{Path: "/dev/vfio/vfio", HostPath: "/dev/vfio/vfio", Type: "c"},
				{Path: vfioDevPath, HostPath: vfioDevPath, Type: "c"},
			}

			logger.Info("Prepared NVMe VFIO", "device", result.Device, "claim", claimUID,
				"pci", allocDev.Info.PCIAddress, "iommuGroup", iommuGroup)
		} else {
			ctrlPath := fmt.Sprintf("/dev/%s", allocDev.Info.Controller)
			deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
				Path: ctrlPath, HostPath: ctrlPath, Type: "c",
			})
			for _, ns := range allocDev.Info.Namespaces {
				deviceNodes = append(deviceNodes, &cdispec.DeviceNode{
					Path: ns.DevicePath, HostPath: ns.DevicePath, Type: "b",
				})
			}

			logger.Info("Prepared NVMe block", "device", result.Device, "claim", claimUID,
				"pci", allocDev.Info.PCIAddress, "namespaces", len(allocDev.Info.Namespaces))
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

	if len(cdiDevices) > 0 {
		specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
		spec := &cdispec.Spec{
			Kind:    cdiKind,
			Devices: cdiDevices,
		}
		minVersion, err := cdiapi.MinimumRequiredVersion(spec)
		if err != nil {
			for _, addr := range vfioBound {
				unprepareVFIO(addr)
			}
			return nil, fmt.Errorf("failed to get CDI spec version: %w", err)
		}
		spec.Version = minVersion

		if err := s.cdiCache.WriteSpec(spec, specName); err != nil {
			for _, addr := range vfioBound {
				unprepareVFIO(addr)
			}
			return nil, fmt.Errorf("failed to write CDI spec: %w", err)
		}
	}

	s.mu.Lock()
	s.prepared[claimUID] = prepared
	s.mu.Unlock()

	if err := s.saveCheckpoint(); err != nil {
		logger.Error(err, "Failed to save checkpoint")
	}

	return prepared, nil
}

func (s *DeviceState) Unprepare(ctx context.Context, claimUID string) error {
	logger := klog.FromContext(ctx)

	s.mu.Lock()
	devices, ok := s.prepared[claimUID]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.prepared, claimUID)
	s.mu.Unlock()

	for _, dev := range devices {
		if dev.IsVFIO && dev.PCIAddress != "" {
			unprepareVFIO(dev.PCIAddress)
		}
	}

	specName := cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, claimUID)
	if err := s.cdiCache.RemoveSpec(specName); err != nil {
		logger.V(2).Info("Failed to remove CDI spec", "claim", claimUID, "err", err)
	}

	if err := s.saveCheckpoint(); err != nil {
		logger.Error(err, "Failed to save checkpoint")
	}

	logger.Info("Unprepared NVMe devices", "claim", claimUID, "devices", len(devices))
	return nil
}

func prepareVFIO(pciAddr string) (string, error) {
	driverPath := fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)
	if target, err := os.Readlink(driverPath); err == nil {
		currentDriver := filepath.Base(target)
		klog.V(2).InfoS("Unbinding device", "pciAddr", pciAddr, "driver", currentDriver)
		unbindPath := fmt.Sprintf("/sys/bus/pci/drivers/%s/unbind", currentDriver)
		if err := os.WriteFile(unbindPath, []byte(pciAddr), 0644); err != nil {
			return "", fmt.Errorf("failed to unbind %s: %w", pciAddr, err)
		}
	}

	overridePath := fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr)
	if err := os.WriteFile(overridePath, []byte("vfio-pci"), 0644); err != nil {
		return "", fmt.Errorf("failed to set driver_override: %w", err)
	}

	bindPath := "/sys/bus/pci/drivers/vfio-pci/bind"
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0644); err != nil {
		if clearErr := os.WriteFile(overridePath, []byte(""), 0644); clearErr != nil {
			klog.ErrorS(clearErr, "Failed to clear driver_override after bind failure", "pciAddr", pciAddr)
		}
		nvmeBindPath := "/sys/bus/pci/drivers/nvme/bind"
		if rebindErr := os.WriteFile(nvmeBindPath, []byte(pciAddr), 0644); rebindErr != nil {
			klog.V(2).InfoS("Failed to restore nvme driver after bind failure", "pciAddr", pciAddr, "err", rebindErr)
		}
		return "", fmt.Errorf("failed to bind to vfio-pci: %w", err)
	}

	if err := os.WriteFile(overridePath, []byte(""), 0644); err != nil {
		klog.V(2).InfoS("Failed to clear driver_override", "pciAddr", pciAddr, "err", err)
	}

	iommuLink := fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)
	target, err := os.Readlink(iommuLink)
	if err != nil {
		return "", fmt.Errorf("failed to read IOMMU group: %w", err)
	}

	return filepath.Base(target), nil
}

func unprepareVFIO(pciAddr string) {
	unbindPath := "/sys/bus/pci/drivers/vfio-pci/unbind"
	if err := os.WriteFile(unbindPath, []byte(pciAddr), 0644); err != nil {
		klog.V(2).InfoS("Failed to unbind from vfio-pci", "pciAddr", pciAddr, "err", err)
	}

	overridePath := fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr)
	_ = os.WriteFile(overridePath, []byte(""), 0644)

	bindPath := "/sys/bus/pci/drivers/nvme/bind"
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0644); err != nil {
		klog.V(2).InfoS("Failed to rebind to nvme", "pciAddr", pciAddr, "err", err)
	}
}

// Checkpoint persistence

type checkpoint struct {
	Prepared map[string][]PreparedNvme `json:"prepared"`
}

func (s *DeviceState) saveCheckpoint() error {
	s.mu.Lock()
	preparedCopy := make(map[string][]PreparedNvme, len(s.prepared))
	for k, v := range s.prepared {
		preparedCopy[k] = append([]PreparedNvme(nil), v...)
	}
	s.mu.Unlock()

	data, err := json.Marshal(checkpoint{Prepared: preparedCopy})
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	dir := filepath.Dir(s.checkpointPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create checkpoint dir: %w", err)
	}

	return os.WriteFile(s.checkpointPath, data, 0600)
}

func (s *DeviceState) restoreCheckpoint(logger klog.Logger) error {
	data, err := os.ReadFile(s.checkpointPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read checkpoint: %w", err)
	}

	var cp checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return fmt.Errorf("unmarshal checkpoint: %w", err)
	}

	if cp.Prepared != nil {
		s.mu.Lock()
		s.prepared = cp.Prepared
		s.mu.Unlock()
		logger.Info("Restored checkpoint", "claims", len(cp.Prepared))
	}
	return nil
}

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
	return append(classConfigs, claimConfigs...), nil
}
