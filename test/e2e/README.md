# E2E Tests for dra-driver-nvme

## Prerequisites

- Kubernetes cluster v1.36+ with DRA feature gate enabled
- Node with physical NVMe devices (not mounted/in-use)
- IOMMU enabled in BIOS and kernel (`intel_iommu=on` or `amd_iommu=on`) for VFIO tests
- The DRA driver deployed: `kubectl apply -f deploy/`
- `KUBECONFIG` pointing to the cluster

## Running

```bash
make test-e2e KUBECONFIG=~/.kube/config
```

Or directly:

```bash
go test -v -count=1 -timeout 10m -tags e2e ./test/e2e/ -kubeconfig=$KUBECONFIG
```

## Test Structure

- `e2e_test.go` — TestMain: client setup and kubeconfig parsing
- `helpers_test.go` — namespace, ResourceClaim, Pod creation helpers with automatic cleanup
- `smoke_test.go` — smoke tests for block and VFIO device modes

## What the Tests Verify

- **TestSmokeBlockDevice**: Creates a ResourceClaim with DeviceClass `dra.nvme`, launches a pod, verifies `/dev/nvme*` devices are visible
- **TestSmokeVFIODevice**: Creates a ResourceClaim with DeviceClass `dra.nvme-vfio`, launches a pod, verifies either legacy VFIO (`/dev/vfio/vfio`) or iommufd (`/dev/iommu`) devices are visible

## Notes

- Tests use `//go:build e2e` so they are excluded from `go test ./...`
- Each test creates an ephemeral namespace that is cleaned up automatically
- Tests skip gracefully if the required DeviceClass is not found in the cluster
