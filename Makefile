# ────────────────────────────────────────────────────────────────────────────
# vault-etcd-sync-operator Makefile
# ────────────────────────────────────────────────────────────────────────────

# Build configuration
IMG            ?= vault-etcd-sync-operator:latest
CONTROLLER_GEN ?= $(shell which controller-gen 2>/dev/null || echo go run sigs.k8s.io/controller-tools/cmd/controller-gen)
ENVTEST        ?= go run sigs.k8s.io/controller-runtime/tools/setup-envtest

# Helm chart location
HELM_CHART_DIR := helm/vault-etcd-sync-operator

# Go build settings
GOFLAGS := -ldflags="-s -w" -trimpath

.PHONY: all
all: build

# ── Code generation ────────────────────────────────────────────────────────────

## generate: Run controller-gen to regenerate DeepCopy methods.
.PHONY: generate
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

## manifests: Generate CRD manifests into config/crd/bases/.
.PHONY: manifests
manifests:
	$(CONTROLLER_GEN) \
		rbac:roleName=vault-etcd-sync-manager-role \
		crd \
		webhook \
		paths="./..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac

# ── Build ──────────────────────────────────────────────────────────────────────

## fmt: Run go fmt.
.PHONY: fmt
fmt:
	go fmt ./...

## vet: Run go vet.
.PHONY: vet
vet:
	go vet ./...

## build: Build the manager binary.
.PHONY: build
build: fmt vet
	CGO_ENABLED=0 go build $(GOFLAGS) -o bin/manager ./cmd/

## run: Run the controller locally (uses kubeconfig from environment).
.PHONY: run
run: fmt vet
	go run ./cmd/ \
		--leader-elect=false \
		--log-level=debug

# ── Testing ────────────────────────────────────────────────────────────────────

## test: Run unit tests (no envtest required).
.PHONY: test
test: fmt vet
	go test -race -count=1 ./... -coverprofile cover.out

## test-integration: Run integration tests with envtest.
.PHONY: test-integration
test-integration: envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use --bin-path $(shell pwd)/bin)" \
		go test -race -count=1 ./... -tags=integration

# ── Docker ─────────────────────────────────────────────────────────────────────

# Arch-suffixed image tags — built natively on each machine then combined
# into a multi-arch manifest.
IMG_AMD64 ?= $(IMG)-amd64
IMG_ARM64 ?= $(IMG)-arm64

## docker-build: Build a native image on the current machine and tag it.
## Run this once on the amd64 host and once on the arm64 host.
.PHONY: docker-build
docker-build:
	docker build --tag $(IMG) .

## docker-build-amd64: Build and tag the amd64 image (run on amd64 host).
.PHONY: docker-build-amd64
docker-build-amd64:
	docker build --tag $(IMG_AMD64) .
	docker push $(IMG_AMD64)

## docker-build-arm64: Build and tag the arm64 image (run on arm64 host).
.PHONY: docker-build-arm64
docker-build-arm64:
	docker build --tag $(IMG_ARM64) .
	docker push $(IMG_ARM64)

## docker-manifest: Combine amd64 + arm64 images into a multi-arch manifest.
## Run this after both arch images have been pushed to the registry.
.PHONY: docker-manifest
docker-manifest:
	docker manifest create $(IMG) \
		--amend $(IMG_AMD64) \
		--amend $(IMG_ARM64)
	docker manifest push $(IMG)

## docker-push: Push a single-arch image built by docker-build.
.PHONY: docker-push
docker-push:
	docker push $(IMG)

# ── Cluster operations ─────────────────────────────────────────────────────────

## install: Install CRDs into the current kubeconfig cluster.
.PHONY: install
install: manifests
	kubectl apply -f config/crd/bases/

## uninstall: Remove CRDs from the current kubeconfig cluster.
.PHONY: uninstall
uninstall:
	kubectl delete -f config/crd/bases/ --ignore-not-found

## deploy: Deploy the operator via Helm (dry-run first, then apply).
.PHONY: deploy
deploy:
	helm upgrade --install vault-etcd-sync-operator $(HELM_CHART_DIR) \
		--namespace vault-etcd-sync \
		--create-namespace \
		--set image.repository=$(shell echo $(IMG) | cut -d: -f1) \
		--set image.tag=$(shell echo $(IMG) | cut -d: -f2) \
		--wait

## undeploy: Remove the Helm release.
.PHONY: undeploy
undeploy:
	helm uninstall vault-etcd-sync-operator --namespace vault-etcd-sync

# ── Helm ───────────────────────────────────────────────────────────────────────

## helm-lint: Lint the Helm chart.
.PHONY: helm-lint
helm-lint:
	helm lint $(HELM_CHART_DIR)

## helm-template: Render the Helm chart to stdout (for review).
.PHONY: helm-template
helm-template:
	helm template vault-etcd-sync-operator $(HELM_CHART_DIR)

## helm-package: Package the Helm chart into a .tgz archive.
.PHONY: helm-package
helm-package: helm-lint
	helm package $(HELM_CHART_DIR) --destination dist/

# ── Utilities ──────────────────────────────────────────────────────────────────

## envtest: Download envtest binaries for integration testing.
.PHONY: envtest
envtest:
	$(ENVTEST) use

## clean: Remove built artifacts.
.PHONY: clean
clean:
	rm -rf bin/ cover.out dist/

## help: Print this help message.
.PHONY: help
help:
	@grep -E '^## ' Makefile | sed 's/## /  /'
