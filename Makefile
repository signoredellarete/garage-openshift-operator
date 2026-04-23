IMG ?= ghcr.io/garage-operator/garage-openshift-operator:latest
NAMESPACE ?= garage-operator-system

# Tooling
CONTROLLER_GEN ?= $(shell which controller-gen 2>/dev/null || echo "go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0")
KUSTOMIZE     ?= $(shell which kustomize 2>/dev/null || echo "go run sigs.k8s.io/kustomize/kustomize/v5@v5.3.0")

.PHONY: all build test manifests generate docker-build docker-push install uninstall deploy undeploy

all: build

## Build the operator binary
build:
	go build -o bin/manager main.go

## Run tests
test:
	go test ./... -coverprofile cover.out

## Generate deepcopy methods and CRD manifests
generate:
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

manifests:
	$(CONTROLLER_GEN) crd rbac:roleName=garage-operator-manager-role webhook paths="./..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac

## Lint
lint:
	golangci-lint run ./...

## Docker
docker-build:
	docker build -t $(IMG) .

docker-push:
	docker push $(IMG)

## Install CRDs into the cluster
install:
	kubectl apply -f config/crd/bases/

## Uninstall CRDs
uninstall:
	kubectl delete -f config/crd/bases/ --ignore-not-found

## Deploy operator to the cluster
deploy:
	kubectl apply -f config/rbac/service_account.yaml
	kubectl apply -f config/rbac/role.yaml
	kubectl apply -f config/rbac/role_binding.yaml
	kubectl apply -f config/manager/manager.yaml

## Remove operator from the cluster
undeploy:
	kubectl delete -f config/manager/manager.yaml --ignore-not-found
	kubectl delete -f config/rbac/role_binding.yaml --ignore-not-found
	kubectl delete -f config/rbac/role.yaml --ignore-not-found
	kubectl delete -f config/rbac/service_account.yaml --ignore-not-found

## Deploy sample GarageCluster
sample:
	kubectl apply -f config/samples/

## Remove sample resources
sample-clean:
	kubectl delete -f config/samples/ --ignore-not-found

## Run operator locally (requires a valid kubeconfig)
run:
	go run ./main.go

## Tidy modules
tidy:
	go mod tidy
