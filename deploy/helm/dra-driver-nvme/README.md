# dra-driver-nvme Helm Chart

Deploys the DRA NVMe driver as a DaemonSet on Kubernetes or OpenShift clusters.

## Prerequisites

- Kubernetes 1.36+ with DRA feature gate enabled, or OpenShift 4.18+
- Nodes with PCIe-attached NVMe devices
- IOMMU enabled in BIOS/kernel for VFIO mode
- Helm 3.x

## Install

```bash
# Kubernetes (default)
helm install dra-nvme deploy/helm/dra-driver-nvme/

# OpenShift
helm install dra-nvme deploy/helm/dra-driver-nvme/ --set platform=openshift

# With a specific version
helm install dra-nvme deploy/helm/dra-driver-nvme/ \
  --set image.tag=v0.1.0

# With custom registry and pull secret
helm install dra-nvme deploy/helm/dra-driver-nvme/ \
  --set image.repository=registry.example.com/dra-driver-nvme \
  --set image.tag=v0.1.0 \
  --set imagePullSecrets[0].name=my-registry-secret
```

## Uninstall

```bash
helm uninstall dra-nvme
```

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `platform` | Target platform: `kubernetes` or `openshift` | `kubernetes` |
| `image.repository` | Container image repository | `ghcr.io/johnahull/dra-driver-nvme` |
| `image.tag` | Container image tag | `latest` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `imagePullSecrets` | Image pull secrets | `[]` |
| `namespace.create` | Create the namespace | `true` |
| `namespace.name` | Namespace name | `dra-nvme` |
| `serviceAccount.create` | Create a service account | `true` |
| `serviceAccount.name` | Service account name override | `""` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `rbac.create` | Create RBAC resources | `true` |
| `deviceClass.block.enabled` | Create block-mode DeviceClass | `true` |
| `deviceClass.block.name` | Block DeviceClass name | `dra.nvme` |
| `deviceClass.vfio.enabled` | Create VFIO-mode DeviceClass | `true` |
| `deviceClass.vfio.name` | VFIO DeviceClass name | `dra.nvme-vfio` |
| `deviceClass.vfio.force` | Allow VFIO with co-grouped devices | `false` |
| `plugin.cdiRoot` | CDI spec directory | `/var/run/cdi` |
| `plugin.extraArgs` | Additional CLI arguments | `[]` |
| `plugin.extraEnv` | Additional environment variables | `[]` |
| `resources` | CPU/memory requests and limits | `{}` |
| `nodeSelector` | Node selector labels | `{}` |
| `tolerations` | Pod tolerations | `[]` |
| `affinity` | Pod affinity rules | `{}` |
| `priorityClassName` | Pod priority class | `""` |
| `podAnnotations` | Pod annotations | `{}` |

## OpenShift

When `platform=openshift`, the chart creates a SecurityContextConstraints resource
that grants the driver's ServiceAccount privileged access. This is required because
the driver needs to:

- Read `/sys` for NVMe device discovery and IOMMU group enumeration
- Write to `/sys` for VFIO driver binding/unbinding
- Access `/dev` for device node exposure via CDI

## DeviceClasses

The chart creates two DeviceClasses by default:

- **`dra.nvme`** — exposes NVMe namespaces as block devices (`/dev/nvme*n*`)
- **`dra.nvme-vfio`** — binds NVMe controllers to vfio-pci for VM passthrough

Disable either with `--set deviceClass.block.enabled=false` or
`--set deviceClass.vfio.enabled=false`.
