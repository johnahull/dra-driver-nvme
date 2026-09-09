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
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/klog/v2"
)

const (
	DriverName = "dra.nvme"
)

type flags struct {
	nodeName                      string
	kubeconfig                    string
	kubeletRegistrarDirectoryPath string
	pluginDataDirectoryPath       string
	cdiRoot                       string
	numaAttrForm                  deviceattribute.AttributeForm
	secureEraseEnabled            bool
}

func main() {
	klog.InitFlags(nil)

	f := &flags{}
	var numaList bool
	flag.StringVar(&f.nodeName, "node-name", "", "Node name (required)")
	flag.StringVar(&f.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (uses in-cluster config if empty)")
	flag.StringVar(&f.kubeletRegistrarDirectoryPath, "kubelet-registrar-path",
		"/var/lib/kubelet/plugins_registry", "Kubelet plugin registrar directory")
	flag.StringVar(&f.pluginDataDirectoryPath, "plugin-data-path",
		"/var/lib/kubelet/plugins/dra.nvme", "Plugin data directory")
	flag.StringVar(&f.cdiRoot, "cdi-root", "/var/run/cdi", "CDI spec directory")
	flag.BoolVar(&numaList, "numa-list", true, "Publish numaNode as SLIT-based list (true) or scalar (false)")
	flag.BoolVar(&f.secureEraseEnabled, "enable-secure-erase", false,
		"Allow claims to request NVMe cryptographic erase on release (opt-in per claim via NvmeConfig.secureErase)")
	flag.Parse()

	if numaList {
		f.numaAttrForm = deviceattribute.ListAttribute
	} else {
		f.numaAttrForm = deviceattribute.ScalarAttribute
	}

	if f.nodeName == "" {
		f.nodeName = os.Getenv("NODE_NAME")
	}
	if f.nodeName == "" {
		klog.Fatal("--node-name or NODE_NAME is required")
	}

	var config *rest.Config
	var err error
	if f.kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", f.kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		klog.Fatalf("Failed to get kube config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create clientset: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	driver, err := NewDriver(ctx, cancel, clientset, f)
	if err != nil {
		klog.Fatalf("Failed to create driver: %v", err)
	}

	logger := klog.FromContext(ctx)
	logger.Info("NVMe DRA driver started", "node", f.nodeName)
	<-ctx.Done()
	logger.Info("Shutting down")
	driver.Shutdown()
}
