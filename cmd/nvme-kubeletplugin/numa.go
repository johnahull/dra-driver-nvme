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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/ptr"
)

// NUMAAttrForm controls whether numaNode is published as a scalar int or an
// ordered list of NUMA nodes derived from the ACPI SLIT distance matrix.
// This will move upstream into k8s.io/dynamic-resource-allocation/deviceattribute
// once KEP-4815 lands; until then we carry the type locally.
type NUMAAttrForm int

const (
	ScalarNUMAAttr NUMAAttrForm = iota
	ListNUMAAttr
)

const numaNodeAttrName resourceapi.QualifiedName = "resource.kubernetes.io/numaNode"

// getNUMANodeAttribute returns a DeviceAttribute for the NUMA topology of the
// PCI device at pciBusID. When form is ListNUMAAttr, the attribute contains a
// SLIT-distance-ordered list of NUMA nodes (physical node first, then
// equidistant peers on the same socket). When form is ScalarNUMAAttr, the
// attribute contains just the physical NUMA node as a scalar int.
func getNUMANodeAttribute(pciBusID string, form NUMAAttrForm) (deviceattribute.DeviceAttribute, error) {
	numaNode, err := readNUMANodeForPCI(pciBusID)
	if err != nil {
		return deviceattribute.DeviceAttribute{}, err
	}

	if form == ScalarNUMAAttr {
		return deviceattribute.DeviceAttribute{
			Name:  numaNodeAttrName,
			Value: resourceapi.DeviceAttribute{IntValue: ptr.To(int64(numaNode))},
		}, nil
	}

	nodes := getNUMANodeList(numaNode)
	return deviceattribute.DeviceAttribute{
		Name:  numaNodeAttrName,
		Value: resourceapi.DeviceAttribute{IntValues: nodes},
	}, nil
}

func readNUMANodeForPCI(pciBusID string) (int, error) {
	numaPath := filepath.Join("/sys/bus/pci/devices", pciBusID, "numa_node")
	data, err := os.ReadFile(numaPath)
	if err != nil {
		return -1, fmt.Errorf("reading NUMA node for %s: %w", pciBusID, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1, fmt.Errorf("parsing NUMA node for %s: %w", pciBusID, err)
	}
	return n, nil
}

// getNUMANodeList returns the NUMA node list for a device, with the physical
// node first followed by equidistant nodes derived from the ACPI SLIT distance
// matrix. Falls back to [N] if SLIT distances are unavailable.
func getNUMANodeList(physicalNode int) []int64 {
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
