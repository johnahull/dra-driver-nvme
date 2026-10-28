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
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

type writeCall struct {
	Path string
	Data string
}

type mockSysfsOps struct {
	links     map[string]string
	linkErrs  map[string]error
	dirs      map[string][]os.DirEntry
	dirErrs   map[string]error
	stats     map[string]bool
	writeErrs map[string]error
	writeErr  func(path, data string) error
	writes    []writeCall
	files     map[string][]byte
	fileErrs  map[string]error
}

func newMockSysfs() *mockSysfsOps {
	return &mockSysfsOps{
		links:     make(map[string]string),
		linkErrs:  make(map[string]error),
		dirs:      make(map[string][]os.DirEntry),
		dirErrs:   make(map[string]error),
		stats:     make(map[string]bool),
		writeErrs: make(map[string]error),
		files:     make(map[string][]byte),
		fileErrs:  make(map[string]error),
	}
}

func (m *mockSysfsOps) ReadLink(path string) (string, error) {
	if err, ok := m.linkErrs[path]; ok {
		return "", err
	}
	if target, ok := m.links[path]; ok {
		return target, nil
	}
	return "", os.ErrNotExist
}

func (m *mockSysfsOps) WriteFile(path string, data []byte, _ fs.FileMode) error {
	m.writes = append(m.writes, writeCall{Path: path, Data: string(data)})
	if m.writeErr != nil {
		if err := m.writeErr(path, string(data)); err != nil {
			return err
		}
	}
	if err, ok := m.writeErrs[path]; ok {
		return err
	}
	return nil
}

func (m *mockSysfsOps) ReadDir(path string) ([]os.DirEntry, error) {
	if err, ok := m.dirErrs[path]; ok {
		return nil, err
	}
	if entries, ok := m.dirs[path]; ok {
		return entries, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockSysfsOps) ReadFile(path string) ([]byte, error) {
	if err, ok := m.fileErrs[path]; ok {
		return nil, err
	}
	if data, ok := m.files[path]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (m *mockSysfsOps) Stat(path string) (fs.FileInfo, error) {
	if exists, ok := m.stats[path]; ok && exists {
		return &fakeFileInfo{}, nil
	}
	return nil, os.ErrNotExist
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

type fakeDirEntry struct {
	name  string
	isDir bool
}

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.isDir }
func (f fakeDirEntry) Type() fs.FileMode          { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func withMockSysfs(t *testing.T, m *mockSysfsOps) {
	t.Helper()
	old := sysfs
	sysfs = m
	t.Cleanup(func() { sysfs = old })
}

func TestCheckIOMMUGroupSafety(t *testing.T) {
	const pciAddr = "0000:3b:00.0"
	iommuLink := fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)
	devicesDir := fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group/devices", pciAddr)

	tests := []struct {
		name          string
		setup         func(*mockSysfsOps)
		wantGroup     string
		wantConflicts int
		wantErr       bool
		wantErrMsg    string
	}{
		{
			name: "single device in group",
			setup: func(m *mockSysfsOps) {
				m.links[iommuLink] = "../../../kernel/iommu_groups/42"
				m.dirs[devicesDir] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
				}
			},
			wantGroup:     "42",
			wantConflicts: 0,
		},
		{
			name: "co-grouped device with no driver",
			setup: func(m *mockSysfsOps) {
				m.links[iommuLink] = "../../../kernel/iommu_groups/42"
				m.dirs[devicesDir] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
				}
			},
			wantGroup:     "42",
			wantConflicts: 0,
		},
		{
			name: "co-grouped device bound to vfio-pci",
			setup: func(m *mockSysfsOps) {
				m.links[iommuLink] = "../../../kernel/iommu_groups/42"
				m.dirs[devicesDir] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
				}
				m.links["/sys/bus/pci/devices/0000:3b:00.1/driver"] = "../../../../bus/pci/drivers/vfio-pci"
			},
			wantGroup:     "42",
			wantConflicts: 0,
		},
		{
			name: "co-grouped device bound to nvme",
			setup: func(m *mockSysfsOps) {
				m.links[iommuLink] = "../../../kernel/iommu_groups/42"
				m.dirs[devicesDir] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
				}
				m.links["/sys/bus/pci/devices/0000:3b:00.1/driver"] = "../../../../bus/pci/drivers/nvme"
			},
			wantGroup:     "42",
			wantConflicts: 1,
		},
		{
			name: "multiple conflicting devices",
			setup: func(m *mockSysfsOps) {
				m.links[iommuLink] = "../../../kernel/iommu_groups/7"
				m.dirs[devicesDir] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
					fakeDirEntry{name: "0000:3b:00.2"},
				}
				m.links["/sys/bus/pci/devices/0000:3b:00.1/driver"] = "../../../../bus/pci/drivers/nvme"
				m.links["/sys/bus/pci/devices/0000:3b:00.2/driver"] = "../../../../bus/pci/drivers/xhci_hcd"
			},
			wantGroup:     "7",
			wantConflicts: 2,
		},
		{
			name: "IOMMU group symlink missing",
			setup: func(m *mockSysfsOps) {
				m.linkErrs[iommuLink] = os.ErrNotExist
			},
			wantErr:    true,
			wantErrMsg: "failed to read IOMMU group",
		},
		{
			name: "ReadDir on group devices fails",
			setup: func(m *mockSysfsOps) {
				m.links[iommuLink] = "../../../kernel/iommu_groups/42"
				m.dirErrs[devicesDir] = fmt.Errorf("permission denied")
			},
			wantErr:    true,
			wantErrMsg: "failed to list IOMMU group",
		},
		{
			name: "co-grouped device driver read returns non-ENOENT error",
			setup: func(m *mockSysfsOps) {
				m.links[iommuLink] = "../../../kernel/iommu_groups/42"
				m.dirs[devicesDir] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
				}
				m.linkErrs["/sys/bus/pci/devices/0000:3b:00.1/driver"] = fmt.Errorf("permission denied")
			},
			wantErr:    true,
			wantErrMsg: "failed to check driver for co-grouped device",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockSysfs()
			tt.setup(m)
			withMockSysfs(t, m)

			groupID, conflicts, err := checkIOMMUGroupSafety(pciAddr)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrMsg != "" && !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q does not contain %q", err, tt.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if groupID != tt.wantGroup {
				t.Errorf("groupID = %q, want %q", groupID, tt.wantGroup)
			}
			if len(conflicts) != tt.wantConflicts {
				t.Errorf("conflicts = %d, want %d: %v", len(conflicts), tt.wantConflicts, conflicts)
			}
		})
	}
}

func TestDetectIOMMUFD(t *testing.T) {
	tests := []struct {
		name   string
		exists bool
		want   bool
	}{
		{"iommufd available", true, true},
		{"iommufd not available", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockSysfs()
			m.stats["/dev/iommu"] = tt.exists
			withMockSysfs(t, m)

			if got := detectIOMMUFD(); got != tt.want {
				t.Errorf("detectIOMMUFD() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindVFIODevice(t *testing.T) {
	const pciAddr = "0000:3b:00.0"
	vfioDevDir := fmt.Sprintf("/sys/bus/pci/devices/%s/vfio-dev", pciAddr)

	tests := []struct {
		name     string
		setup    func(*mockSysfsOps)
		wantPath string
		wantErr  bool
	}{
		{
			name: "found vfio0",
			setup: func(m *mockSysfsOps) {
				m.dirs[vfioDevDir] = []os.DirEntry{fakeDirEntry{name: "vfio0"}}
			},
			wantPath: "/dev/vfio/devices/vfio0",
		},
		{
			name: "found vfio5",
			setup: func(m *mockSysfsOps) {
				m.dirs[vfioDevDir] = []os.DirEntry{fakeDirEntry{name: "vfio5"}}
			},
			wantPath: "/dev/vfio/devices/vfio5",
		},
		{
			name: "empty vfio-dev directory",
			setup: func(m *mockSysfsOps) {
				m.dirs[vfioDevDir] = []os.DirEntry{}
			},
			wantErr: true,
		},
		{
			name: "no vfio-dev directory",
			setup: func(m *mockSysfsOps) {
				m.dirErrs[vfioDevDir] = os.ErrNotExist
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockSysfs()
			tt.setup(m)
			withMockSysfs(t, m)

			path, err := findVFIODevice(pciAddr)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func setupHappyPathMock(m *mockSysfsOps, pciAddr string) {
	m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)] = "../../../kernel/iommu_groups/42"
	m.dirs[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group/devices", pciAddr)] = []os.DirEntry{
		fakeDirEntry{name: pciAddr},
	}
	m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
}

func TestClearDriverOverride(t *testing.T) {
	const pciAddr = "0000:3b:00.0"
	m := newMockSysfs()
	withMockSysfs(t, m)

	if err := clearDriverOverride(pciAddr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := writeCall{
		Path: fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr),
		Data: "\n",
	}
	if len(m.writes) != 1 || m.writes[0] != want {
		t.Fatalf("writes = %v, want [%v]", m.writes, want)
	}
}

func TestRestoreNvmeVerifiesDriver(t *testing.T) {
	const pciAddr = "0000:3b:00.0"
	m := newMockSysfs()
	m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/vfio-pci"
	withMockSysfs(t, m)

	err := restoreNvme(pciAddr)
	if err == nil {
		t.Fatal("expected driver verification error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to verify nvme rebind") {
		t.Errorf("error %q does not contain driver verification message", err)
	}
}

func TestPrepareVFIO(t *testing.T) {
	const pciAddr = "0000:3b:00.0"

	tests := []struct {
		name        string
		pciAddr     string
		force       bool
		setup       func(*mockSysfsOps)
		wantGroup   string
		wantErr     bool
		wantErrMsg  string
		checkWrites func(t *testing.T, writes []writeCall)
	}{
		{
			name:    "happy path: unbind nvme, bind vfio-pci",
			pciAddr: pciAddr,
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
			},
			wantGroup: "42",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				expected := []writeCall{
					{"/sys/bus/pci/drivers/nvme/unbind", pciAddr},
					{fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr), "vfio-pci"},
					{"/sys/bus/pci/drivers/vfio-pci/bind", pciAddr},
					{fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr), "\n"},
				}
				if len(writes) != len(expected) {
					t.Fatalf("got %d writes, want %d:\n  got:  %v\n  want: %v", len(writes), len(expected), writes, expected)
				}
				for i, w := range writes {
					if w.Path != expected[i].Path || w.Data != expected[i].Data {
						t.Errorf("write[%d] = {%q, %q}, want {%q, %q}", i, w.Path, w.Data, expected[i].Path, expected[i].Data)
					}
				}
			},
		},
		{
			name:    "no existing driver (no-op unbind)",
			pciAddr: pciAddr,
			setup: func(m *mockSysfsOps) {
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)] = "../../../kernel/iommu_groups/42"
				m.dirs[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group/devices", pciAddr)] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
				}
			},
			wantGroup: "42",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				if len(writes) != 3 {
					t.Fatalf("got %d writes, want 3 (override, bind, clear): %v", len(writes), writes)
				}
				if writes[0].Data != "vfio-pci" {
					t.Errorf("first write should be driver_override=vfio-pci, got %q", writes[0].Data)
				}
			},
		},
		{
			name:    "vfio-pci bind fails: verify rollback",
			pciAddr: pciAddr,
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
				m.writeErrs["/sys/bus/pci/drivers/vfio-pci/bind"] = fmt.Errorf("device busy")
			},
			wantErr:    true,
			wantErrMsg: "failed to bind to vfio-pci",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				var paths []string
				for _, w := range writes {
					paths = append(paths, w.Path)
				}
				wantPaths := []string{
					"/sys/bus/pci/drivers/nvme/unbind",
					fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr),
					"/sys/bus/pci/drivers/vfio-pci/bind",
					fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr),
					"/sys/bus/pci/drivers/nvme/bind",
				}
				if len(paths) != len(wantPaths) {
					t.Fatalf("writes = %v, want %v", paths, wantPaths)
				}
				for i, p := range paths {
					if p != wantPaths[i] {
						t.Errorf("write[%d] path = %q, want %q", i, p, wantPaths[i])
					}
				}
				if writes[3].Data != "\n" {
					t.Errorf("rollback should clear driver_override with newline, got %q", writes[3].Data)
				}
			},
		},
		{
			name:    "driver_override clear fails: verify rollback",
			pciAddr: pciAddr,
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
				m.writeErr = func(path, data string) error {
					if path == fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr) && data == "\n" {
						return fmt.Errorf("read-only fs")
					}
					return nil
				}
			},
			wantErr:    true,
			wantErrMsg: "failed to clear driver_override",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				if len(writes) != 7 {
					t.Fatalf("got %d writes, want 7: %v", len(writes), writes)
				}
				if writes[3].Data != "\n" || writes[5].Data != "\n" {
					t.Errorf("rollback clear writes = %q and %q, want newline", writes[3].Data, writes[5].Data)
				}
				if writes[6].Path != "/sys/bus/pci/drivers/nvme/bind" {
					t.Errorf("last write path = %q, want nvme bind", writes[6].Path)
				}
			},
		},
		{
			name:    "driver_override fails",
			pciAddr: pciAddr,
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
				m.writeErrs[fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr)] = fmt.Errorf("read-only fs")
			},
			wantErr:    true,
			wantErrMsg: "failed to set driver_override",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				wantPaths := []string{
					"/sys/bus/pci/drivers/nvme/unbind",
					fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr),
					fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr),
					"/sys/bus/pci/drivers/nvme/bind",
				}
				if len(writes) != len(wantPaths) {
					t.Fatalf("writes = %v, want %v", writes, wantPaths)
				}
				for i, wantPath := range wantPaths {
					if writes[i].Path != wantPath {
						t.Errorf("write[%d] path = %q, want %q", i, writes[i].Path, wantPath)
					}
				}
				if writes[2].Data != "\n" {
					t.Errorf("rollback should clear driver_override with newline, got %q", writes[2].Data)
				}
			},
		},
		{
			name:    "co-grouped device blocks binding",
			pciAddr: pciAddr,
			force:   false,
			setup: func(m *mockSysfsOps) {
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)] = "../../../kernel/iommu_groups/42"
				m.dirs[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group/devices", pciAddr)] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
				}
				m.links["/sys/bus/pci/devices/0000:3b:00.1/driver"] = "../../../../bus/pci/drivers/nvme"
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
			},
			wantErr:    true,
			wantErrMsg: "has devices bound to non-vfio-pci drivers",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				if len(writes) != 0 {
					t.Errorf("expected no writes when safety check blocks, got %d: %v", len(writes), writes)
				}
			},
		},
		{
			name:    "co-grouped device with force override",
			pciAddr: pciAddr,
			force:   true,
			setup: func(m *mockSysfsOps) {
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)] = "../../../kernel/iommu_groups/42"
				m.dirs[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group/devices", pciAddr)] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
				}
				m.links["/sys/bus/pci/devices/0000:3b:00.1/driver"] = "../../../../bus/pci/drivers/nvme"
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
			},
			wantGroup: "42",
		},
		{
			name:    "IOMMU group check fails",
			pciAddr: pciAddr,
			setup: func(m *mockSysfsOps) {
				m.linkErrs[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group", pciAddr)] = os.ErrNotExist
			},
			wantErr:    true,
			wantErrMsg: "IOMMU group safety check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockSysfs()
			tt.setup(m)
			withMockSysfs(t, m)

			result, err := prepareVFIO(tt.pciAddr, tt.force)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrMsg != "" && !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q does not contain %q", err, tt.wantErrMsg)
				}
				if tt.checkWrites != nil {
					tt.checkWrites(t, m.writes)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IOMMUGroup != tt.wantGroup {
				t.Errorf("IOMMUGroup = %q, want %q", result.IOMMUGroup, tt.wantGroup)
			}
			if tt.checkWrites != nil {
				tt.checkWrites(t, m.writes)
			}
		})
	}
}

func TestPrepareVFIOWithIOMMUFD(t *testing.T) {
	const pciAddr = "0000:3b:00.0"
	vfioDevDir := fmt.Sprintf("/sys/bus/pci/devices/%s/vfio-dev", pciAddr)

	tests := []struct {
		name           string
		setup          func(*mockSysfsOps)
		wantIOMMUFD    bool
		wantDevicePath string
		wantGroup      string
	}{
		{
			name: "iommufd path: returns device path",
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
				m.stats["/dev/iommu"] = true
				m.dirs[vfioDevDir] = []os.DirEntry{fakeDirEntry{name: "vfio0"}}
			},
			wantIOMMUFD:    true,
			wantDevicePath: "/dev/vfio/devices/vfio0",
			wantGroup:      "42",
		},
		{
			name: "iommufd detected but vfio-dev read fails: falls back to legacy",
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
				m.stats["/dev/iommu"] = true
				m.dirErrs[vfioDevDir] = os.ErrNotExist
			},
			wantIOMMUFD:    false,
			wantDevicePath: "",
			wantGroup:      "42",
		},
		{
			name: "no iommufd: legacy path",
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
			},
			wantIOMMUFD:    false,
			wantDevicePath: "",
			wantGroup:      "42",
		},
		{
			name: "iommufd with co-grouped device: warns instead of blocking",
			setup: func(m *mockSysfsOps) {
				setupHappyPathMock(m, pciAddr)
				m.dirs[fmt.Sprintf("/sys/bus/pci/devices/%s/iommu_group/devices", pciAddr)] = []os.DirEntry{
					fakeDirEntry{name: pciAddr},
					fakeDirEntry{name: "0000:3b:00.1"},
				}
				m.links["/sys/bus/pci/devices/0000:3b:00.1/driver"] = "../../../../bus/pci/drivers/nvme"
				m.stats["/dev/iommu"] = true
				m.dirs[vfioDevDir] = []os.DirEntry{fakeDirEntry{name: "vfio3"}}
			},
			wantIOMMUFD:    true,
			wantDevicePath: "/dev/vfio/devices/vfio3",
			wantGroup:      "42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockSysfs()
			tt.setup(m)
			withMockSysfs(t, m)

			result, err := prepareVFIO(pciAddr, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.UseIOMMUFD != tt.wantIOMMUFD {
				t.Errorf("UseIOMMUFD = %v, want %v", result.UseIOMMUFD, tt.wantIOMMUFD)
			}
			if result.DevicePath != tt.wantDevicePath {
				t.Errorf("DevicePath = %q, want %q", result.DevicePath, tt.wantDevicePath)
			}
			if result.IOMMUGroup != tt.wantGroup {
				t.Errorf("IOMMUGroup = %q, want %q", result.IOMMUGroup, tt.wantGroup)
			}
		})
	}
}

func TestUnprepareVFIO(t *testing.T) {
	const pciAddr = "0000:3b:00.0"

	tests := []struct {
		name        string
		setup       func(*mockSysfsOps)
		wantErrMsg  string
		checkWrites func(t *testing.T, writes []writeCall)
	}{
		{
			name: "successful unbind and rebind",
			setup: func(m *mockSysfsOps) {
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
			},
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				if len(writes) != 3 {
					t.Fatalf("got %d writes, want 3: %v", len(writes), writes)
				}
				if writes[0].Path != "/sys/bus/pci/drivers/vfio-pci/unbind" {
					t.Errorf("write[0] = %q, want unbind path", writes[0].Path)
				}
				if writes[1].Data != "\n" {
					t.Errorf("write[1] should clear driver_override, got %q", writes[1].Data)
				}
				if writes[2].Path != "/sys/bus/pci/drivers/nvme/bind" {
					t.Errorf("write[2] = %q, want nvme bind path", writes[2].Path)
				}
			},
		},
		{
			name: "unbind fails: still clears override and tries rebind",
			setup: func(m *mockSysfsOps) {
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
				m.writeErrs["/sys/bus/pci/drivers/vfio-pci/unbind"] = fmt.Errorf("device busy")
			},
			wantErrMsg: "failed to unbind from vfio-pci",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				if len(writes) != 3 {
					t.Fatalf("got %d writes, want 3 (all attempts even on failure): %v", len(writes), writes)
				}
			},
		},
		{
			name: "driver_override clear fails",
			setup: func(m *mockSysfsOps) {
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
				m.writeErrs[fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr)] = fmt.Errorf("read-only fs")
			},
			wantErrMsg: "failed to clear driver_override",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				if len(writes) != 3 {
					t.Fatalf("got %d writes, want 3: %v", len(writes), writes)
				}
				if writes[1].Data != "\n" {
					t.Errorf("clear write = %q, want newline", writes[1].Data)
				}
			},
		},
		{
			name: "nvme rebind fails",
			setup: func(m *mockSysfsOps) {
				m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
				m.writeErrs["/sys/bus/pci/drivers/nvme/bind"] = fmt.Errorf("no such device")
			},
			wantErrMsg: "failed to rebind to nvme",
			checkWrites: func(t *testing.T, writes []writeCall) {
				t.Helper()
				if len(writes) != 3 {
					t.Fatalf("got %d writes, want 3: %v", len(writes), writes)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockSysfs()
			tt.setup(m)
			withMockSysfs(t, m)

			err := unprepareVFIO(pciAddr)
			if tt.wantErrMsg == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErrMsg != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q does not contain %q", err, tt.wantErrMsg)
				}
			}

			if tt.checkWrites != nil {
				tt.checkWrites(t, m.writes)
			}
		})
	}
}

func TestUnprepareResourceClaimsReportsVFIOCleanupError(t *testing.T) {
	const (
		claimUID = "claim-uid"
		pciAddr  = "0000:3b:00.0"
	)
	m := newMockSysfs()
	m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
	m.writeErrs[fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", pciAddr)] = fmt.Errorf("read-only fs")
	withMockSysfs(t, m)

	s := &DeviceState{
		prepared: map[string][]*PreparedNvme{
			claimUID: {{IsVFIO: true, PCIAddress: pciAddr}},
		},
	}
	d := &driver{state: s}
	result, err := d.UnprepareResourceClaims(context.Background(), []kubeletplugin.NamespacedObject{
		{UID: types.UID(claimUID)},
	})
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if err := result[types.UID(claimUID)]; err == nil {
		t.Fatal("expected per-claim cleanup error, got nil")
	} else if !strings.Contains(err.Error(), "failed to clear driver_override") {
		t.Errorf("error %q does not contain failed clear message", err)
	}
}

func TestUnprepareSavesPartialCleanupCheckpoint(t *testing.T) {
	const claimUID = "claim-uid"
	const firstPCI = "0000:3b:00.0"
	const secondPCI = "0000:3b:00.1"

	m := newMockSysfs()
	for _, pciAddr := range []string{firstPCI, secondPCI} {
		m.links[fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddr)] = "../../../../bus/pci/drivers/nvme"
	}
	m.writeErrs[fmt.Sprintf("/sys/bus/pci/devices/%s/driver_override", secondPCI)] = fmt.Errorf("read-only fs")
	withMockSysfs(t, m)

	s := &DeviceState{
		prepared: map[string][]*PreparedNvme{
			claimUID: {
				{IsVFIO: true, PCIAddress: firstPCI},
				{IsVFIO: true, PCIAddress: secondPCI},
			},
		},
		checkpointPath: filepath.Join(t.TempDir(), "prepared-claims.json"),
	}
	if err := s.Unprepare(context.Background(), claimUID); err == nil {
		t.Fatal("expected cleanup error, got nil")
	}

	data, err := os.ReadFile(s.checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	var cp checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		t.Fatalf("unmarshal checkpoint: %v", err)
	}
	remaining := cp.Prepared[claimUID]
	if len(remaining) != 1 || remaining[0].PCIAddress != secondPCI {
		t.Fatalf("checkpoint remaining devices = %#v, want only %s", remaining, secondPCI)
	}
}

func TestUnprepareRejectsConcurrentCleanup(t *testing.T) {
	const claimUID = "claim-uid"
	s := &DeviceState{
		prepared: map[string][]*PreparedNvme{
			claimUID: {{IsVFIO: true, PCIAddress: "0000:3b:00.0"}},
		},
		unpreparing: map[string]bool{claimUID: true},
	}

	err := s.Unprepare(context.Background(), claimUID)
	if err == nil {
		t.Fatal("expected concurrent cleanup error, got nil")
	}
	if !strings.Contains(err.Error(), "already being unprepared") {
		t.Errorf("error %q does not contain concurrent cleanup message", err)
	}
}

func TestCheckpointBackwardCompat(t *testing.T) {
	raw := `{"prepared":{"uid1":[{"isVFIO":true,"pciAddress":"0000:3b:00.0"}]}}`
	var cp checkpoint
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	devices := cp.Prepared["uid1"]
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].UseIOMMUFD {
		t.Error("expected UseIOMMUFD=false for old checkpoint without the field")
	}
	if !devices[0].IsVFIO {
		t.Error("expected IsVFIO=true")
	}
	if devices[0].PCIAddress != "0000:3b:00.0" {
		t.Errorf("PCIAddress = %q, want 0000:3b:00.0", devices[0].PCIAddress)
	}
}

func TestCheckpointWithIOMMUFD(t *testing.T) {
	original := checkpoint{
		Prepared: map[string][]*PreparedNvme{
			"uid1": {
				{IsVFIO: true, PCIAddress: "0000:3b:00.0", UseIOMMUFD: true},
			},
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var restored checkpoint
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !restored.Prepared["uid1"][0].UseIOMMUFD {
		t.Error("UseIOMMUFD not preserved through marshal/unmarshal")
	}
}
