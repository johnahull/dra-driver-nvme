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

//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestSmokeBlockDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	skipIfNoDeviceClass(t, ctx, "dra.nvme")

	ns := createNamespace(t, ctx)
	claimName := createResourceClaim(t, ctx, ns, "dra.nvme")
	podName := createPodWithClaim(t, ctx, ns, claimName, []string{
		"sh", "-c", "ls -la /dev/nvme* && echo SMOKE_OK",
	})

	phase := waitForPodDone(t, ctx, ns, podName, 3*time.Minute)
	logs := getPodLogs(t, ctx, ns, podName)
	requirePhase(t, phase, corev1.PodSucceeded, logs)

	if !strings.Contains(logs, "SMOKE_OK") {
		t.Errorf("expected SMOKE_OK in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, "/dev/nvme") {
		t.Errorf("expected /dev/nvme* devices in logs, got:\n%s", logs)
	}
}

func TestSmokeVFIODevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	skipIfNoDeviceClass(t, ctx, "dra.nvme-vfio")

	ns := createNamespace(t, ctx)
	claimName := createResourceClaim(t, ctx, ns, "dra.nvme-vfio")
	podName := createPodWithClaim(t, ctx, ns, claimName, []string{
		"sh", "-c",
		"ls -la /dev/vfio/ /dev/iommu 2>/dev/null; ls -la /dev/vfio/devices/ 2>/dev/null; echo VFIO_OK",
	})

	phase := waitForPodDone(t, ctx, ns, podName, 3*time.Minute)
	logs := getPodLogs(t, ctx, ns, podName)
	requirePhase(t, phase, corev1.PodSucceeded, logs)

	if !strings.Contains(logs, "VFIO_OK") {
		t.Errorf("expected VFIO_OK in logs, got:\n%s", logs)
	}

	hasLegacy := strings.Contains(logs, "/dev/vfio/vfio")
	hasIOMMUFD := strings.Contains(logs, "/dev/iommu")
	if !hasLegacy && !hasIOMMUFD {
		t.Errorf("expected either /dev/vfio/vfio (legacy) or /dev/iommu (iommufd) in logs, got:\n%s", logs)
	}
}
