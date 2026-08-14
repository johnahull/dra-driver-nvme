HELM_CHART := deploy/helm/dra-driver-nvme
CONTROLLER_GEN_VERSION ?= v0.21.0
BIN_DIR := $(CURDIR)/bin
CONTROLLER_GEN := $(BIN_DIR)/controller-gen

.PHONY: build test test-unit test-e2e lint vet helm-lint helm-template generate generate-deepcopy

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

generate: generate-deepcopy

generate-deepcopy: $(CONTROLLER_GEN)
	rm -f api/zz_generated.deepcopy.go
	$(CONTROLLER_GEN) \
		object:headerFile=hack/boilerplate.go.txt \
		paths=./api/ \
		output:object:dir=./api/

$(CONTROLLER_GEN):
	mkdir -p $(BIN_DIR)
	GOBIN=$(BIN_DIR) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
