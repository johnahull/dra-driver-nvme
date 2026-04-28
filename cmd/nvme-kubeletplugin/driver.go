package main

import (
	"context"
	"fmt"
	"os"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	drametadatav1alpha1 "k8s.io/dynamic-resource-allocation/api/metadata/v1alpha1"
	"k8s.io/klog/v2"
)

type driver struct {
	helper    *kubeletplugin.Helper
	state     *DeviceState
	cancelCtx context.CancelFunc
}

func NewDriver(ctx context.Context, cancel context.CancelFunc, clientset kubernetes.Interface, f *flags) (*driver, error) {
	logger := klog.FromContext(ctx)

	d := &driver{cancelCtx: cancel}

	state, err := NewDeviceState(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("error initializing device state: %w", err)
	}
	d.state = state

	podUID := os.Getenv("POD_UID")
	opts := []kubeletplugin.Option{
		kubeletplugin.KubeClient(clientset),
		kubeletplugin.NodeName(f.nodeName),
		kubeletplugin.DriverName(DriverName),
		kubeletplugin.RegistrarDirectoryPath(f.kubeletRegistrarDirectoryPath),
		kubeletplugin.PluginDataDirectoryPath(f.pluginDataDirectoryPath),
		kubeletplugin.EnableDeviceMetadata(true),
		kubeletplugin.MetadataVersions(drametadatav1alpha1.SchemeGroupVersion),
	}
	if podUID != "" {
		opts = append(opts, kubeletplugin.RollingUpdate(types.UID(podUID)))
	}

	helper, err := kubeletplugin.Start(ctx, d, opts...)
	if err != nil {
		return nil, fmt.Errorf("error starting kubelet plugin: %w", err)
	}
	d.helper = helper

	sortedNames := state.allocatable.SortedNames()
	devices := make([]resourceapi.Device, 0, len(sortedNames))
	for _, name := range sortedNames {
		devices = append(devices, state.allocatable[name].GetDevice(name))
	}

	resources := resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			f.nodeName: {
				Slices: []resourceslice.Slice{
					{Devices: devices},
				},
			},
		},
	}

	if err := helper.PublishResources(ctx, resources); err != nil {
		return nil, fmt.Errorf("error publishing resources: %w", err)
	}

	logger.Info("Published NVMe devices", "count", len(devices))
	return d, nil
}

func (d *driver) Shutdown() {
	d.helper.Stop()
}

func (d *driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	logger := klog.FromContext(ctx)
	logger.Info("PrepareResourceClaims", "count", len(claims))
	result := make(map[types.UID]kubeletplugin.PrepareResult)

	for _, claim := range claims {
		result[claim.UID] = d.prepareClaim(ctx, claim)
	}

	return result, nil
}

func (d *driver) prepareClaim(ctx context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	logger := klog.FromContext(ctx)

	preparedDevices, err := d.state.Prepare(ctx, claim)
	if err != nil {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("error preparing NVMe devices for claim %v: %w", claim.UID, err),
		}
	}

	var devices []kubeletplugin.Device
	for _, pd := range preparedDevices {
		dev := kubeletplugin.Device{
			Requests:     pd.RequestNames,
			PoolName:     pd.PoolName,
			DeviceName:   pd.DeviceName,
			CDIDeviceIDs: pd.CdiDeviceIds,
		}

		if allocDev, exists := d.state.allocatable[pd.DeviceName]; exists {
			pci := allocDev.Info.PCIAddress
			numa := int64(allocDev.Info.NUMANode)
			model := allocDev.Info.Model
			dev.Metadata = &kubeletplugin.DeviceMetadata{
				Attributes: map[string]resourceapi.DeviceAttribute{
					"resource.kubernetes.io/pciBusID": {StringValue: &pci},
					"numaNode":                       {IntValue: &numa},
					"model":                          {StringValue: &model},
				},
			}
		}

		devices = append(devices, dev)
	}

	logger.V(2).Info("Prepared claim", "claimUID", claim.UID, "devices", len(devices))
	return kubeletplugin.PrepareResult{Devices: devices}
}

func (d *driver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	logger := klog.FromContext(ctx)
	logger.Info("UnprepareResourceClaims", "count", len(claims))
	result := make(map[types.UID]error)

	for _, claim := range claims {
		result[claim.UID] = d.state.Unprepare(ctx, string(claim.UID))
	}

	return result, nil
}

func (d *driver) HandleError(ctx context.Context, err error, msg string) {
	logger := klog.FromContext(ctx)
	logger.Error(err, msg)
	d.cancelCtx()
}
