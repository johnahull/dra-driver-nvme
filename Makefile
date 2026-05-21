HELM_CHART := deploy/helm/dra-driver-nvme

.PHONY: build test test-unit test-e2e lint vet helm-lint helm-template

build:
	CGO_ENABLED=0 go build -o nvme-kubeletplugin ./cmd/nvme-kubeletplugin/

test: test-unit

test-unit:
	go test ./... -count=1

test-e2e:
	go test -v -count=1 -timeout 10m -tags e2e ./test/e2e/ -kubeconfig=$(KUBECONFIG)

vet:
	go vet ./...

lint: vet helm-lint

helm-lint:
	helm lint $(HELM_CHART)

helm-template:
	helm template test $(HELM_CHART)
