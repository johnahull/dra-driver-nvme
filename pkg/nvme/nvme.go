package nvme

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

// DeviceInfo holds information about an NVMe device discovered from sysfs.
type DeviceInfo struct {
	// Controller name (e.g., "nvme0")
	Controller string
	// PCI address (e.g., "0000:3b:00.0")
	PCIAddress string
	// NUMA node
	NUMANode int
	// CPU socket ID (physical package)
	CPUSocketID int
	// Model name
	Model string
	// Serial number
	Serial string
	// Firmware revision
	FirmwareRev string
	// Transport type (e.g., "pcie")
	Transport string
	// Namespaces on this controller
	Namespaces []NamespaceInfo
}

// NamespaceInfo holds information about an NVMe namespace.
type NamespaceInfo struct {
	// Namespace name (e.g., "nvme0n1")
	Name string
	// Block device path (e.g., "/dev/nvme0n1")
	DevicePath string
	// Size in bytes
	SizeBytes uint64
}

// Discover enumerates all local PCIe-attached NVMe devices from sysfs.
func Discover() ([]DeviceInfo, error) {
	controllers, err := filepath.Glob("/sys/class/nvme/nvme*")
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate NVMe controllers: %w", err)
	}

	var devices []DeviceInfo
	for _, ctrlPath := range controllers {
		ctrlName := filepath.Base(ctrlPath)

		// Get PCI address from device symlink
		deviceLink := filepath.Join(ctrlPath, "device")
		pciPath, err := os.Readlink(deviceLink)
		if err != nil {
			klog.V(2).InfoS("Skipping controller: cannot read device symlink", "controller", ctrlName, "err", err)
			continue
		}
		pciAddr := filepath.Base(pciPath)

		// Skip non-PCI devices (e.g., NVMe over Fabrics)
		if !isPCIAddress(pciAddr) {
			klog.V(2).InfoS("Skipping controller: not a PCIe device", "controller", ctrlName, "address", pciAddr)
			continue
		}

		// Read NUMA node
		numaNode := readIntFile(filepath.Join(deviceLink, "numa_node"), -1)
		if numaNode < 0 {
			klog.V(2).InfoS("Skipping controller: invalid NUMA node", "controller", ctrlName)
			continue
		}

		// Read device attributes
		model := readStringFile(filepath.Join(ctrlPath, "model"))
		serial := readStringFile(filepath.Join(ctrlPath, "serial"))
		firmwareRev := readStringFile(filepath.Join(ctrlPath, "firmware_rev"))
		transport := readStringFile(filepath.Join(ctrlPath, "transport"))

		// Derive CPU socket ID from NUMA node
		cpuSocketID := getSocketIDForNUMA(numaNode)

		dev := DeviceInfo{
			Controller:  ctrlName,
			PCIAddress:  pciAddr,
			NUMANode:    numaNode,
			CPUSocketID: cpuSocketID,
			Model:       sanitize(model),
			Serial:      sanitize(serial),
			FirmwareRev: sanitize(firmwareRev),
			Transport:   sanitize(transport),
		}

		// Discover namespaces
		nsMatches, _ := filepath.Glob(fmt.Sprintf("/sys/block/%sn*", ctrlName))
		for _, nsPath := range nsMatches {
			nsName := filepath.Base(nsPath)
			sizeStr := readStringFile(filepath.Join(nsPath, "size"))
			sectors, _ := strconv.ParseUint(sizeStr, 10, 64)
			sizeBytes := sectors * 512

			dev.Namespaces = append(dev.Namespaces, NamespaceInfo{
				Name:       nsName,
				DevicePath: "/dev/" + nsName,
				SizeBytes:  sizeBytes,
			})
		}

		if len(dev.Namespaces) == 0 {
			klog.V(2).InfoS("Skipping controller: no namespaces found", "controller", ctrlName)
			continue
		}

		if hasMountedNamespace(dev.Namespaces) {
			klog.InfoS("Skipping controller: has mounted namespace (likely boot/root disk)",
				"controller", ctrlName, "pci", pciAddr)
			continue
		}

		klog.InfoS("Discovered NVMe controller",
			"controller", ctrlName, "pci", pciAddr,
			"numa", numaNode, "model", dev.Model, "namespaces", len(dev.Namespaces))
		devices = append(devices, dev)
	}

	return devices, nil
}

func isPCIAddress(s string) bool {
	// PCI address format: DDDD:BB:DD.F (e.g., 0000:3b:00.0)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return false
	}
	// Last segment must contain a dot (DD.F)
	return strings.Contains(parts[2], ".")
}

func readStringFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readIntFile(path string, defaultVal int) int {
	s := readStringFile(path)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}

func sanitize(s string) string {
	r := strings.NewReplacer(" ", "_", "(", "", ")", "", "\t", "")
	return r.Replace(s)
}

// hasMountedNamespace checks if any namespace on an NVMe controller has
// mounted partitions or active device-mapper entries. Reads /proc/1/mounts
// to see the host's mount table (works inside containers). Falls back to
// /proc/mounts if /proc/1/mounts is unavailable.
func hasMountedNamespace(namespaces []NamespaceInfo) bool {
	var mounts []byte
	var err error
	for _, path := range []string{"/proc/1/mounts", "/proc/mounts"} {
		mounts, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}
	if err != nil {
		return false
	}
	mountStr := string(mounts)
	for _, ns := range namespaces {
		if strings.Contains(mountStr, ns.DevicePath) || strings.Contains(mountStr, ns.Name) {
			return true
		}
		// Check for LVM/device-mapper on partitions (e.g., nvme0n1p3 → dm-0)
		parts, _ := filepath.Glob(fmt.Sprintf("/sys/block/%s/%s*", ns.Name, ns.Name))
		for _, part := range parts {
			partName := filepath.Base(part)
			if strings.Contains(mountStr, partName) {
				return true
			}
			holders, _ := filepath.Glob(filepath.Join(part, "holders", "*"))
			if len(holders) > 0 {
				return true
			}
		}
	}
	return false
}

// getSocketIDForNUMA derives the CPU physical_package_id (socket) for a NUMA node
// by reading the socket ID of the first CPU on that NUMA node.
func getSocketIDForNUMA(numaNode int) int {
	cpuListPath := fmt.Sprintf("/sys/devices/system/node/node%d/cpulist", numaNode)
	cpuList := readStringFile(cpuListPath)
	if cpuList == "" {
		return 0
	}
	// Parse first CPU from the list (e.g., "0,2,4,6" or "0-31")
	firstCPU := cpuList
	if idx := strings.IndexAny(cpuList, ",-"); idx > 0 {
		firstCPU = cpuList[:idx]
	}
	socketPath := fmt.Sprintf("/sys/devices/system/cpu/cpu%s/topology/physical_package_id", firstCPU)
	return readIntFile(socketPath, 0)
}
