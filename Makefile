CONTROLLER_GEN_VERSION := v0.21.0
NAMESPACE ?= default

.PHONY: build test vet fmt generate manifests run
build:
	go build -o bin/milos-controller ./cmd/controller

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w api internal cmd tests

generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object paths="./api/..."

manifests:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) crd rbac:roleName=milos-controller paths="./api/...;./internal/controller/..." output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

run:
	go run ./cmd/controller --namespace=$(NAMESPACE)

.PHONY: test-integration
test-integration:
	go test -tags=integration -count=1 -timeout=5m ./tests/integration/...
