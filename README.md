# DRA NVMe Driver

A Kubernetes [Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) driver for NVMe devices. Discovers local PCIe-attached NVMe controllers and namespaces, publishes them as DRA resources with topology attributes, and supports both block device exposure and VFIO passthrough for VM use cases.

## Features

- **NVMe discovery from sysfs** — enumerates all PCIe-attached NVMe controllers and their namespaces
- **Standardized topology attributes** — publishes `resource.kubernetes.io/numaNode`, `cpuSocketID`, `pciBusID`, and `pcieRoot` for cross-driver NUMA alignment via DRA `matchAttribute` constraints
- **Block device mode** (default) — exposes `/dev/nvme*n*` block devices to pods via CDI
- **VFIO passthrough mode** — binds NVMe controllers to `vfio-pci` for direct VM passthrough via KubeVirt
- **Device metadata (KEP-5304)** — publishes `pciBusID` metadata for KubeVirt guest topology placement

## Published Attributes

Each NVMe device is published as a ResourceSlice with these attributes:

| Attribute | Type | Example | Description |
|-----------|------|---------|-------------|
| `resource.kubernetes.io/numaNode` | int | `0` | Host NUMA node |
| `resource.kubernetes.io/cpuSocketID` | int | `0` | CPU socket ID |
| `resource.kubernetes.io/pciBusID` | string | `0000:3b:00.0` | PCI bus address |
| `resource.kubernetes.io/pcieRoot` | string | `pci0000:36` | PCIe root complex |
| `dra.nvme/numaNode` | int | `0` | NUMA node (vendor-specific) |
| `dra.nvme/model` | string | `SAMSUNG MZQL21T9HCJR` | Device model |
| `dra.nvme/serial` | string | `S64GNE...` | Serial number |
| `dra.nvme/transport` | string | `pcie` | Transport type |
| `dra.nvme/namespaceCount` | int | `1` | Number of namespaces |

## Quick Start

### Deploy

```bash
kubectl apply -f deploy/daemonset.yaml
kubectl apply -f deploy/deviceclass.yaml
```

This creates:
- `dra-nvme` namespace with the driver DaemonSet
- `dra.nvme` DeviceClass (block device mode)
- `dra.nvme-vfio` DeviceClass (VFIO passthrough mode)

### Allocate an NVMe device

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: nvme-claim
spec:
  spec:
    devices:
      requests:
      - name: nvme
        exactly:
          deviceClassName: dra.nvme
```

### NUMA-aligned allocation with GPU

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: gpu-nvme-aligned
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: vfio.gpu.nvidia.com
      - name: nvme
        exactly:
          deviceClassName: dra.nvme
      constraints:
      - requests: ["gpu", "nvme"]
        matchAttribute: resource.kubernetes.io/numaNode
```

### VFIO passthrough for KubeVirt VMs

Use the `dra.nvme-vfio` DeviceClass to bind the NVMe controller to `vfio-pci`:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: nvme-vfio
spec:
  spec:
    devices:
      requests:
      - name: nvme
        exactly:
          deviceClassName: dra.nvme-vfio
```

## DeviceClasses

| DeviceClass | Mode | Description |
|-------------|------|-------------|
| `dra.nvme` | block | Exposes `/dev/nvme*n*` block devices via CDI |
| `dra.nvme-vfio` | vfio | Binds controller to `vfio-pci`, exposes `/dev/vfio/*` for VM passthrough |

## Building

```bash
# Build binary
go build -o nvme-kubeletplugin ./cmd/nvme-kubeletplugin/

# Build container image
podman build -t dra-driver-nvme:latest .
```

## Project Structure

```
cmd/nvme-kubeletplugin/    # DRA kubelet plugin entrypoint
pkg/nvme/                  # NVMe sysfs discovery
api/                       # NvmeConfig API types (block/vfio mode selection)
deploy/                    # Kubernetes manifests (DaemonSet, DeviceClass, RBAC)
```

## Related Projects

- [dranet](https://github.com/kubernetes-sigs/dranet) — DRA network driver (NICs, SR-IOV VFs)
- [dra-driver-cpu](https://github.com/kubernetes-sigs/dra-driver-cpu) — DRA CPU driver
- [dra-driver-memory](https://github.com/ffromani/dra-driver-memory) — DRA memory/hugepages driver
- [dra-driver-nvidia-gpu](https://github.com/NVIDIA/k8s-dra-driver-gpu) — NVIDIA GPU DRA driver

## License

Apache License 2.0. See [LICENSE](LICENSE).
