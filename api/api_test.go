package api

import (
	"testing"
)

func TestNvmeConfigNormalize(t *testing.T) {
	tests := []struct {
		name     string
		config   *NvmeConfig
		wantMode string
		wantErr  bool
	}{
		{
			name:     "empty mode defaults to block",
			config:   &NvmeConfig{},
			wantMode: "block",
		},
		{
			name:     "block mode unchanged",
			config:   &NvmeConfig{Mode: "block"},
			wantMode: "block",
		},
		{
			name:     "vfio mode unchanged",
			config:   &NvmeConfig{Mode: "vfio"},
			wantMode: "vfio",
		},
		{
			name:    "nil config errors",
			config:  nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Normalize()
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.config.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", tt.config.Mode, tt.wantMode)
			}
		})
	}
}

func TestNvmeConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{name: "block valid", mode: "block"},
		{name: "vfio valid", mode: "vfio"},
		{name: "empty invalid", mode: "", wantErr: true},
		{name: "unknown invalid", mode: "passthrough", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &NvmeConfig{Mode: tt.mode}
			err := c.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultNvmeConfig(t *testing.T) {
	c := DefaultNvmeConfig()
	if c.Mode != "block" {
		t.Errorf("default mode = %q, want \"block\"", c.Mode)
	}
	if c.Kind != NvmeConfigKind {
		t.Errorf("kind = %q, want %q", c.Kind, NvmeConfigKind)
	}
	if c.APIVersion != GroupName+"/"+Version {
		t.Errorf("apiVersion = %q, want %q", c.APIVersion, GroupName+"/"+Version)
	}
}

func TestDeepCopyObject(t *testing.T) {
	c := &NvmeConfig{Mode: "vfio"}
	copy := c.DeepCopyObject().(*NvmeConfig)
	if copy.Mode != "vfio" {
		t.Errorf("copy mode = %q, want \"vfio\"", copy.Mode)
	}
	copy.Mode = "block"
	if c.Mode != "vfio" {
		t.Error("DeepCopy mutated original")
	}
}

func TestDecoder(t *testing.T) {
	if Decoder == nil {
		t.Fatal("Decoder not initialized")
	}

	raw := []byte(`{"apiVersion":"nvme.dra.io/v1alpha1","kind":"NvmeConfig","mode":"vfio"}`)
	obj, _, err := Decoder.Decode(raw, nil, nil)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	config, ok := obj.(*NvmeConfig)
	if !ok {
		t.Fatalf("decoded object is %T, want *NvmeConfig", obj)
	}
	if config.Mode != "vfio" {
		t.Errorf("mode = %q, want \"vfio\"", config.Mode)
	}
}
