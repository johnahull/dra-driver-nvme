package api

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
)

const (
	GroupName = "nvme.dra.io"
	Version   = "v1alpha1"

	NvmeConfigKind = "NvmeConfig"
)

var Decoder runtime.Decoder

// NvmeConfig holds configuration for an NVMe device allocation.
type NvmeConfig struct {
	metav1.TypeMeta `json:",inline"`
	// Mode selects the device exposure mode.
	// "block" (default) exposes /dev/nvme*n* block devices.
	// "vfio" binds to vfio-pci for VM passthrough.
	Mode string `json:"mode,omitempty"`
}

func (c *NvmeConfig) DeepCopyObject() runtime.Object {
	copy := *c
	return &copy
}

func DefaultNvmeConfig() *NvmeConfig {
	return &NvmeConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       NvmeConfigKind,
		},
		Mode: "block",
	}
}

func (c *NvmeConfig) Normalize() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if c.Mode == "" {
		c.Mode = "block"
	}
	return nil
}

func (c *NvmeConfig) Validate() error {
	switch c.Mode {
	case "block", "vfio":
		return nil
	default:
		return fmt.Errorf("invalid mode %q: must be \"block\" or \"vfio\"", c.Mode)
	}
}

func init() {
	scheme := runtime.NewScheme()
	schemeGroupVersion := schema.GroupVersion{
		Group:   GroupName,
		Version: Version,
	}
	scheme.AddKnownTypes(schemeGroupVersion, &NvmeConfig{})
	metav1.AddToGroupVersion(scheme, schemeGroupVersion)

	Decoder = json.NewSerializerWithOptions(
		json.DefaultMetaFactory,
		scheme,
		scheme,
		json.SerializerOptions{Pretty: true, Strict: true},
	)
}
