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
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	nvmeapi "github.com/johnahull/dra-driver-nvme/api"
	"github.com/johnahull/dra-driver-nvme/pkg/nvme"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

func nvmeConfigRaw(t *testing.T, secureErase bool) []byte {
	t.Helper()
	cfg := &nvmeapi.NvmeConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: nvmeapi.GroupName + "/" + nvmeapi.Version,
			Kind:       nvmeapi.NvmeConfigKind,
		},
		Mode:        "block",
		SecureErase: secureErase,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal NvmeConfig: %v", err)
	}
	return data
}

func newTestClaim(t *testing.T, claimUID, deviceName string, secureErase bool) *resourceapi.ResourceClaim {
	t.Helper()
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(claimUID)},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Request: "req0", Driver: DriverName, Pool: "test-node", Device: deviceName},
					},
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source: resourceapi.AllocationConfigSourceClaim,
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     DriverName,
									Parameters: runtime.RawExtension{Raw: nvmeConfigRaw(t, secureErase)},
								},
							},
						},
					},
				},
			},
		},
	}
}

// newTestDeviceStateWithController builds a DeviceState with one allocatable
// controller device (no namespaces need real sysfs backing for these tests,
// since VFIO is not exercised here) plus real, temp-dir-backed CDI cache and
// checkpoint path.
func newTestDeviceStateWithController(t *testing.T, secureEraseEnabled bool) *DeviceState {
	t.Helper()
	ctrl := testController()

	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(t.TempDir()))
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	return &DeviceState{
		allocatable:        AllocatableDevices{ctrl.Controller: {Info: ctrl}},
		prepared:           make(map[string][]*PreparedNvme),
		preparing:          make(map[string]bool),
		unpreparing:        make(map[string]bool),
		cdiCache:           cache,
		checkpointPath:     filepath.Join(t.TempDir(), "prepared-claims.json"),
		secureEraseEnabled: secureEraseEnabled,
		baseCtx:            t.Context(),
	}
}

func TestPrepareSecureEraseGate(t *testing.T) {
	t.Run("rejected when driver flag disabled", func(t *testing.T) {
		s := newTestDeviceStateWithController(t, false)
		claim := newTestClaim(t, "uid1", "nvme0", true)

		_, err := s.Prepare(t.Context(), claim)
		if err == nil {
			t.Fatal("Prepare() = nil error, want rejection")
		}
		if _, ok := s.prepared["uid1"]; ok {
			t.Error("claim should not be recorded as prepared")
		}
	})

	t.Run("allowed when driver flag enabled", func(t *testing.T) {
		s := newTestDeviceStateWithController(t, true)
		claim := newTestClaim(t, "uid1", "nvme0", true)

		prepared, err := s.Prepare(t.Context(), claim)
		if err != nil {
			t.Fatalf("Prepare() = %v, want nil", err)
		}
		if len(prepared) != 1 || !prepared[0].SecureErase {
			t.Errorf("prepared = %+v, want one device with SecureErase=true", prepared)
		}
	})

	t.Run("not requested is always allowed", func(t *testing.T) {
		s := newTestDeviceStateWithController(t, false)
		claim := newTestClaim(t, "uid1", "nvme0", false)

		prepared, err := s.Prepare(t.Context(), claim)
		if err != nil {
			t.Fatalf("Prepare() = %v, want nil", err)
		}
		if len(prepared) != 1 || prepared[0].SecureErase {
			t.Errorf("prepared = %+v, want one device with SecureErase=false", prepared)
		}
	})
}

func TestUnprepareFailClosedOnEraseFailure(t *testing.T) {
	withShrunkDevicePresenceTiming(t)
	oldTimeout := secureEraseTimeout
	secureEraseTimeout = 200 * time.Millisecond
	t.Cleanup(func() { secureEraseTimeout = oldTimeout })

	m := newMockSysfs()
	m.stats["/dev/nvme0n1"] = true
	m.stats["/dev/nvme0n2"] = true
	withMockSysfs(t, m)

	runner := &fakeCommandRunner{errFor: map[string]error{"/dev/nvme0n1": errors.New("invalid field (crypto erase unsupported)")}}
	withFakeCommandRunner(t, runner)

	s := newTestDeviceStateWithController(t, true)
	s.prepared["uid1"] = []*PreparedNvme{
		{
			Device:      drapbv1.Device{DeviceName: "nvme0", PoolName: "test-node"},
			SecureErase: true,
		},
	}

	err := s.Unprepare(t.Context(), "uid1")
	if err == nil {
		t.Fatal("Unprepare() = nil, want error on erase failure")
	}
	if _, ok := s.prepared["uid1"]; !ok {
		t.Error("claim should remain in prepared map after failed erase (fail-closed)")
	}
	if s.unpreparing["uid1"] {
		t.Error("unpreparing guard should be cleared after failure so a retry can proceed")
	}

	if len(runner.calls) != 1 {
		t.Fatalf("got %d nvme calls before retry, want 1 (should stop at the first failing namespace)", len(runner.calls))
	}

	// Retry should attempt erase again (idempotent) for BOTH namespaces
	// (retry restarts the device's erase loop from the top), and succeed
	// once the failure is cleared.
	runner.errFor = nil
	if err := s.Unprepare(t.Context(), "uid1"); err != nil {
		t.Fatalf("retry Unprepare() = %v, want nil", err)
	}
	if len(runner.calls) != 3 {
		t.Errorf("got %d total nvme calls after retry, want 3 (1 failed + 2 re-attempted on retry) — "+
			"proves the retry actually re-ran erase rather than just clearing state", len(runner.calls))
	}
	if _, ok := s.prepared["uid1"]; ok {
		t.Error("claim should be removed from prepared map after successful erase")
	}
}

func TestUnprepareSuccessCleansUp(t *testing.T) {
	withShrunkDevicePresenceTiming(t)

	m := newMockSysfs()
	m.stats["/dev/nvme0n1"] = true
	m.stats["/dev/nvme0n2"] = true
	withMockSysfs(t, m)

	runner := &fakeCommandRunner{}
	withFakeCommandRunner(t, runner)

	s := newTestDeviceStateWithController(t, true)
	s.prepared["uid1"] = []*PreparedNvme{
		{
			Device:      drapbv1.Device{DeviceName: "nvme0", PoolName: "test-node"},
			SecureErase: true,
		},
	}

	if err := s.Unprepare(t.Context(), "uid1"); err != nil {
		t.Fatalf("Unprepare() = %v, want nil", err)
	}
	if _, ok := s.prepared["uid1"]; ok {
		t.Error("claim should be removed from prepared map")
	}
	if len(runner.calls) != 2 {
		t.Errorf("got %d nvme calls, want 2 (one per namespace)", len(runner.calls))
	}
}

func TestUnprepareWithoutSecureEraseSkipsErase(t *testing.T) {
	runner := &fakeCommandRunner{}
	withFakeCommandRunner(t, runner)

	s := newTestDeviceStateWithController(t, false)
	s.prepared["uid1"] = []*PreparedNvme{
		{Device: drapbv1.Device{DeviceName: "nvme0", PoolName: "test-node"}},
	}

	if err := s.Unprepare(t.Context(), "uid1"); err != nil {
		t.Fatalf("Unprepare() = %v, want nil", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("got %d nvme calls, want 0 (secure erase not requested)", len(runner.calls))
	}
}

func TestUnprepareMixedSecureEraseFlags(t *testing.T) {
	withShrunkDevicePresenceTiming(t)

	ctrl0 := testController()
	ctrl1 := testController()
	ctrl1.Controller = "nvme1"
	ctrl1.PCIAddress = "0000:3c:00.0"
	ctrl1.Namespaces = []nvme.NamespaceInfo{{Name: "nvme1n1", DevicePath: "/dev/nvme1n1", SizeBytes: 1024}}

	m := newMockSysfs()
	m.stats["/dev/nvme0n1"] = true
	m.stats["/dev/nvme0n2"] = true
	m.stats["/dev/nvme1n1"] = true
	withMockSysfs(t, m)

	runner := &fakeCommandRunner{}
	withFakeCommandRunner(t, runner)

	s := newTestDeviceStateWithController(t, true)
	s.allocatable[ctrl1.Controller] = &AllocatableDevice{Info: ctrl1}
	s.prepared["uid1"] = []*PreparedNvme{
		{Device: drapbv1.Device{DeviceName: ctrl0.Controller, PoolName: "test-node"}, SecureErase: true},
		{Device: drapbv1.Device{DeviceName: ctrl1.Controller, PoolName: "test-node"}, SecureErase: false},
	}

	if err := s.Unprepare(t.Context(), "uid1"); err != nil {
		t.Fatalf("Unprepare() = %v, want nil", err)
	}
	if _, ok := s.prepared["uid1"]; ok {
		t.Error("claim should be removed from prepared map")
	}
	// Only nvme0's 2 namespaces should be erased; nvme1 opted out.
	if len(runner.calls) != 2 {
		t.Fatalf("got %d nvme calls, want 2 (only the SecureErase=true device's namespaces)", len(runner.calls))
	}
	for _, c := range runner.calls {
		if c.args[1] == "/dev/nvme1n1" {
			t.Errorf("nvme1n1 should not have been erased (SecureErase=false on that device): %+v", c)
		}
	}
}

func TestUnprepareConcurrentGuard(t *testing.T) {
	s := newTestDeviceStateWithController(t, false)
	s.prepared["uid1"] = []*PreparedNvme{
		{Device: drapbv1.Device{DeviceName: "nvme0", PoolName: "test-node"}},
	}
	s.unpreparing["uid1"] = true

	err := s.Unprepare(t.Context(), "uid1")
	if err == nil {
		t.Fatal("Unprepare() = nil, want error when already unpreparing")
	}
	if _, ok := s.prepared["uid1"]; !ok {
		t.Error("concurrent guard should not have touched the prepared map")
	}
}

func TestPrepareBlockedWhileUnpreparing(t *testing.T) {
	s := newTestDeviceStateWithController(t, false)
	s.prepared["uid1"] = []*PreparedNvme{
		{Device: drapbv1.Device{DeviceName: "nvme0", PoolName: "test-node"}},
	}
	s.unpreparing["uid1"] = true

	claim := newTestClaim(t, "uid1", "nvme0", false)
	_, err := s.Prepare(t.Context(), claim)
	if err == nil {
		t.Fatal("Prepare() = nil, want error while claim is being unprepared")
	}
}

func TestCheckpointSchemaVersion(t *testing.T) {
	t.Run("current save round-trips schema version", func(t *testing.T) {
		data, err := json.Marshal(checkpoint{
			SchemaVersion: checkpointSchemaVersion,
			Prepared:      map[string][]*PreparedNvme{"uid1": {{SecureErase: true}}},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var cp checkpoint
		if err := json.Unmarshal(data, &cp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if cp.SchemaVersion != checkpointSchemaVersion {
			t.Errorf("SchemaVersion = %d, want %d", cp.SchemaVersion, checkpointSchemaVersion)
		}
		if !cp.Prepared["uid1"][0].SecureErase {
			t.Error("SecureErase not preserved through marshal/unmarshal")
		}
	})

	t.Run("old checkpoint without schema version defaults to 0 and SecureErase false", func(t *testing.T) {
		raw := `{"prepared":{"uid1":[{"isVFIO":false,"pciAddress":"0000:3b:00.0"}]}}`
		var cp checkpoint
		if err := json.Unmarshal([]byte(raw), &cp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if cp.SchemaVersion != 0 {
			t.Errorf("SchemaVersion = %d, want 0 for pre-secure-erase checkpoint", cp.SchemaVersion)
		}
		if cp.Prepared["uid1"][0].SecureErase {
			t.Error("expected SecureErase=false default for old checkpoint entry")
		}
	})
}
