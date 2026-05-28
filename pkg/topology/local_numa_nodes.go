package topology

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GetNUMANodeList returns the NUMA node list for a device, with the physical
// node first followed by other equidistant nodes derived from the ACPI SLIT
// distance matrix.
//
// For CPU/memory devices (which ARE a NUMA node), this returns [N].
// For I/O devices that are equidistant to multiple NUMA nodes (e.g., a PCIe
// device behind a shared I/O die on AMD EPYC under NPS4), this returns the
// physical node first, then the remaining equidistant nodes:
// e.g., physical=6, equidistant=[4,5,6,7] → [6, 4, 5, 7]
//
// The socket filter prevents cross-socket matches on systems where SLIT
// distances are flat within a socket.
//
// Falls back to [N] if SLIT distances are unavailable.
func GetNUMANodeList(physicalNode int) []int64 {
	if physicalNode < 0 {
		return []int64{int64(physicalNode)}
	}

	equidistant, err := getEquidistantNUMANodes(physicalNode)
	if err != nil || len(equidistant) <= 1 {
		return []int64{int64(physicalNode)}
	}

	result := []int64{int64(physicalNode)}
	for _, n := range equidistant {
		if n != physicalNode {
			result = append(result, int64(n))
		}
	}
	return result
}

func getEquidistantNUMANodes(node int) ([]int, error) {
	distPath := filepath.Join("/sys/devices/system/node", fmt.Sprintf("node%d", node), "distance")
	data, err := os.ReadFile(distPath)
	if err != nil {
		return nil, err
	}

	fields := strings.Fields(strings.TrimSpace(string(data)))
	if node >= len(fields) {
		return nil, fmt.Errorf("NUMA node %d out of range", node)
	}

	distances := make([]int, len(fields))
	for i, f := range fields {
		d, err := strconv.Atoi(f)
		if err != nil {
			return nil, err
		}
		distances[i] = d
	}

	minDist := -1
	for i, d := range distances {
		if i == node {
			continue
		}
		if minDist == -1 || d < minDist {
			minDist = d
		}
	}

	if minDist == -1 {
		return []int{node}, nil
	}

	reportedSocket, haveSocket := getSocketForNUMANode(node)

	var nodes []int
	for i, d := range distances {
		if i == node {
			nodes = append(nodes, i)
			continue
		}
		if d != minDist {
			continue
		}
		if haveSocket {
			candidateSocket, ok := getSocketForNUMANode(i)
			if ok && candidateSocket != reportedSocket {
				continue
			}
		}
		nodes = append(nodes, i)
	}

	sort.Ints(nodes)
	return nodes, nil
}

func getSocketForNUMANode(node int) (int, bool) {
	cpulistPath := filepath.Join("/sys/devices/system/node", fmt.Sprintf("node%d", node), "cpulist")
	data, err := os.ReadFile(cpulistPath)
	if err != nil {
		return -1, false
	}

	cpuStr := strings.TrimSpace(string(data))
	if cpuStr == "" {
		return -1, false
	}

	firstCPU := strings.Split(strings.Split(cpuStr, ",")[0], "-")[0]
	cpuID, err := strconv.Atoi(firstCPU)
	if err != nil {
		return -1, false
	}

	pkgPath := filepath.Join("/sys/devices/system/cpu", fmt.Sprintf("cpu%d", cpuID), "topology", "physical_package_id")
	pkgData, err := os.ReadFile(pkgPath)
	if err != nil {
		return -1, false
	}

	socketID, err := strconv.Atoi(strings.TrimSpace(string(pkgData)))
	if err != nil {
		return -1, false
	}

	return socketID, true
}
