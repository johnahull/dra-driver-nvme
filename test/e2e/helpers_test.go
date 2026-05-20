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
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

var resourceClaimGVR = schema.GroupVersionResource{
	Group:    "resource.k8s.io",
	Version:  "v1",
	Resource: "resourceclaims",
}

func createNamespace(t *testing.T, ctx context.Context) string {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "dra-nvme-e2e-",
		},
	}
	created, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), created.Name, metav1.DeleteOptions{})
	})
	return created.Name
}

func createResourceClaim(t *testing.T, ctx context.Context, ns, className string) string {
	t.Helper()
	claim := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "resource.k8s.io/v1",
			"kind":       "ResourceClaim",
			"metadata": map[string]interface{}{
				"generateName": "nvme-e2e-",
				"namespace":    ns,
			},
			"spec": map[string]interface{}{
				"devices": map[string]interface{}{
					"requests": []interface{}{
						map[string]interface{}{
							"name":            "nvme",
							"deviceClassName": className,
							"count":           int64(1),
						},
					},
				},
			},
		},
	}

	created, err := dynClient.Resource(resourceClaimGVR).Namespace(ns).Create(ctx, claim, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create ResourceClaim: %v", err)
	}
	t.Cleanup(func() {
		_ = dynClient.Resource(resourceClaimGVR).Namespace(ns).Delete(context.Background(), created.GetName(), metav1.DeleteOptions{})
	})
	return created.GetName()
}

func createPodWithClaim(t *testing.T, ctx context.Context, ns, claimName string, command []string) string {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "dra-nvme-e2e-",
			Namespace:    ns,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "test",
					Image:   "registry.access.redhat.com/ubi9/ubi-minimal:latest",
					Command: command,
				},
			},
			ResourceClaims: []corev1.PodResourceClaim{
				{
					Name:              "nvme",
					ResourceClaimName: &claimName,
				},
			},
		},
	}

	created, err := clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(ns).Delete(context.Background(), created.Name, metav1.DeleteOptions{})
	})
	return created.Name
}

func waitForPodDone(t *testing.T, ctx context.Context, ns, name string, timeout time.Duration) corev1.PodPhase {
	t.Helper()
	var phase corev1.PodPhase
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		phase = pod.Status.Phase
		return phase == corev1.PodSucceeded || phase == corev1.PodFailed, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for pod %s/%s (last phase: %s): %v", ns, name, phase, err)
	}
	return phase
}

func getPodLogs(t *testing.T, ctx context.Context, ns, name string) string {
	t.Helper()
	req := clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		t.Fatalf("failed to stream pod logs: %v", err)
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		t.Fatalf("failed to read pod logs: %v", err)
	}
	return buf.String()
}

func requirePhase(t *testing.T, got corev1.PodPhase, want corev1.PodPhase, logs string) {
	t.Helper()
	if got != want {
		t.Fatalf("pod phase = %s, want %s\nlogs:\n%s", got, want, logs)
	}
}

func skipIfNoDeviceClass(t *testing.T, ctx context.Context, className string) {
	t.Helper()
	deviceClassGVR := schema.GroupVersionResource{
		Group:    "resource.k8s.io",
		Version:  "v1",
		Resource: "deviceclasses",
	}
	_, err := dynClient.Resource(deviceClassGVR).Get(ctx, className, metav1.GetOptions{})
	if err != nil {
		t.Skipf("DeviceClass %q not found, skipping: %v", className, err)
	}
	_ = fmt.Sprintf("DeviceClass %s found", className)
}
