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
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	drametadatav1alpha1 "k8s.io/dynamic-resource-allocation/api/metadata/v1alpha1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

type driver struct {
	helper       *kubeletplugin.Helper
	state        *DeviceState
	cancelCtx    context.CancelFunc
	numaAttrForm deviceattribute.AttributeForm
}

func NewDriver(ctx context.Context, cancel context.CancelFunc, clientset kubernetes.Interface, f *flags) (*driver, error) {
	logger := klog.FromContext(ctx)

	d := &driver{cancelCtx: cancel, numaAttrForm: f.numaAttrForm}

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
		kubeletplugin.EnableDeviceMetadata(true, []schema.GroupVersion{drametadatav1alpha1.SchemeGroupVersion}),
	}
	if podUID != "" {
		opts = append(opts, kubeletplugin.RollingUpdate(types.UID(podUID)))
	}

	helper, err := kubeletplugin.Start(ctx, d, opts...)
	if err != nil {
		return nil, fmt.Errorf("error starting kubelet plugin: %w", err)
	}
	d.helper = helper

	resources := buildDriverResources(state.allocatable, f.nodeName, f.numaAttrForm, clientset)

	if err := helper.PublishResources(ctx, resources); err != nil {
		return nil, fmt.Errorf("error publishing resources: %w", err)
	}

	logger.Info("Published NVMe devices", "allocatable", len(state.allocatable))
	return d, nil
}

func buildDriverResources(allocatable AllocatableDevices, nodeName string, attrForm deviceattribute.AttributeForm, clientset kubernetes.Interface) resourceslice.DriverResources {
	sortedNames := allocatable.SortedNames()
	devices := make([]resourceapi.Device, 0, len(sortedNames))
	for _, name := range sortedNames {
		devices = append(devices, allocatable[name].GetDevice(name, attrForm))
	}

	counterSets, hasCounterSets := collectNVMeCounterSets(allocatable)

	if !hasCounterSets {
		return resourceslice.DriverResources{
			Pools: map[string]resourceslice.Pool{
				nodeName: {
					Slices: []resourceslice.Slice{
						{Devices: devices},
					},
				},
			},
		}
	}

	useSplit := shouldUseSplitResourceSlices(clientset)

	if useSplit {
		return resourceslice.DriverResources{
			Pools: map[string]resourceslice.Pool{
				nodeName: {
					Slices: []resourceslice.Slice{
						{SharedCounters: counterSets},
						{Devices: devices},
					},
				},
			},
		}
	}

	return resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			nodeName: {
				Slices: []resourceslice.Slice{
					{
						SharedCounters: counterSets,
						Devices:        devices,
					},
				},
			},
		},
	}
}

func collectNVMeCounterSets(allocatable AllocatableDevices) ([]resourceapi.CounterSet, bool) {
	counterSetsByCtrl := make(map[string]*resourceapi.CounterSet)

	for _, device := range allocatable {
		ctrlName := device.Info.Controller
		if _, seen := counterSetsByCtrl[ctrlName]; !seen {
			cs := device.GetSharedCounterSet()
			if cs != nil {
				counterSetsByCtrl[ctrlName] = cs
			}
		}
	}

	if len(counterSetsByCtrl) == 0 {
		return nil, false
	}

	ctrlNames := make([]string, 0, len(counterSetsByCtrl))
	for name := range counterSetsByCtrl {
		ctrlNames = append(ctrlNames, name)
	}
	sort.Strings(ctrlNames)

	result := make([]resourceapi.CounterSet, 0, len(counterSetsByCtrl))
	for _, name := range ctrlNames {
		result = append(result, *counterSetsByCtrl[name])
	}
	return result, true
}

func shouldUseSplitResourceSlices(client kubernetes.Interface) bool {
	v, err := client.Discovery().ServerVersion()
	if err != nil {
		klog.Warningf("Failed to detect K8s version for ResourceSlice format; defaulting to split slices: %v", err)
		return true
	}

	minor, err := strconv.Atoi(strings.TrimSuffix(v.Minor, "+"))
	if err != nil {
		klog.Warningf("Failed to parse K8s minor version %q; defaulting to split slices: %v", v.Minor, err)
		return true
	}

	if minor < 35 {
		klog.V(2).Infof("K8s version %s.%s (< 1.35): using combined ResourceSlices", v.Major, v.Minor)
		return false
	}
	klog.V(2).Infof("K8s version %s.%s (>= 1.35): using split ResourceSlices", v.Major, v.Minor)
	return true
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

func (d *driver) buildDeviceMetadata(allocDev *AllocatableDevice) map[string]resourceapi.DeviceAttribute {
	attrs := map[string]resourceapi.DeviceAttribute{
		"dra.nvme/model":       {StringValue: &allocDev.Info.Model},
		"dra.nvme/serial":      {StringValue: &allocDev.Info.Serial},
		"dra.nvme/firmwareRev": {StringValue: &allocDev.Info.FirmwareRev},
		"dra.nvme/transport":   {StringValue: &allocDev.Info.Transport},
		"dra.nvme/numaNode":    {IntValue: ptr.To(int64(allocDev.Info.NUMANode))},
	}

	// Add upstream standardized attributes (use pre-validated helpers from AllocatableDevice)
	if allocDev.pciBusIDAttr.Name != "" {
		attrs[string(allocDev.pciBusIDAttr.Name)] = allocDev.pciBusIDAttr.Value
	}

	numaAttr, err := deviceattribute.GetNUMANodeAttributeByPCIBusID(allocDev.Info.PCIAddress, d.numaAttrForm)
	if err == nil {
		attrs[string(numaAttr.Name)] = numaAttr.Value
	}

	if allocDev.pcieRootAttr.Name != "" {
		attrs[string(allocDev.pcieRootAttr.Name)] = allocDev.pcieRootAttr.Value
	}

	// Add namespace-specific attributes for namespace devices
	if allocDev.IsNamespaceDevice() {
		nsName := allocDev.Namespace.Name
		ctrlName := allocDev.Info.Controller
		attrs["dra.nvme/namespaceName"] = resourceapi.DeviceAttribute{StringValue: &nsName}
		attrs["dra.nvme/controllerName"] = resourceapi.DeviceAttribute{StringValue: &ctrlName}
	}

	return attrs
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
			dev.Metadata = &kubeletplugin.DeviceMetadata{
				Attributes: d.buildDeviceMetadata(allocDev),
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

func (d *driver) WatchHealthStatus(ctx context.Context, reports chan<- kubeletplugin.DeviceHealthReport) error {
	// Health reporting is not supported by this driver
	return kubeletplugin.ErrHealthNotSupported
}
