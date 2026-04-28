package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	DriverName = "dra.nvme"
)

type flags struct {
	nodeName                      string
	kubeletRegistrarDirectoryPath string
	pluginDataDirectoryPath       string
	cdiRoot                       string
}

func main() {
	klog.InitFlags(nil)

	f := &flags{}
	flag.StringVar(&f.nodeName, "node-name", "", "Node name (required)")
	flag.StringVar(&f.kubeletRegistrarDirectoryPath, "kubelet-registrar-path",
		"/var/lib/kubelet/plugins_registry", "Kubelet plugin registrar directory")
	flag.StringVar(&f.pluginDataDirectoryPath, "plugin-data-path",
		"/var/lib/kubelet/plugins/dra.nvme", "Plugin data directory")
	flag.StringVar(&f.cdiRoot, "cdi-root", "/var/run/cdi", "CDI spec directory")
	flag.Parse()

	if f.nodeName == "" {
		f.nodeName = os.Getenv("NODE_NAME")
	}
	if f.nodeName == "" {
		klog.Fatal("--node-name or NODE_NAME is required")
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Failed to get in-cluster config: %v", err)
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
