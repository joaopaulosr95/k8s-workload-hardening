TAG ?= dev
IMAGE ?= ghcr.io/joaopaulosr95/k8s-workload-hardening
CLUSTER ?= hardening

.PHONY: test cover image kind-up kind-down deploy samples verify verify-crd verify-crd-hardening

test:
	go test ./pkg/... -race

cover:
	go test ./pkg/... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

image:
	docker build -t $(IMAGE):$(TAG) .

kind-up:
	kind create cluster --name $(CLUSTER) --config hack/kind/cluster.yaml

kind-down:
	kind delete cluster --name $(CLUSTER)

deploy: image
	kind load docker-image $(IMAGE):$(TAG) --name $(CLUSTER)
	kubectl apply -f deploy/crd.yaml
	kubectl apply -f deploy/rbac.yaml
	kubectl apply -f deploy/controller.yaml
	kubectl -n isolation-system rollout restart deployment/network-isolation
	kubectl -n isolation-system rollout status deployment/network-isolation --timeout=120s

samples:
	kubectl apply -f deploy/samples/workloads.yaml
	kubectl -n tenant-a wait --for=condition=Ready pod/gateway --timeout=120s
	kubectl -n tenant-b wait --for=condition=Ready pod/dashboard --timeout=120s
	kubectl -n tenant-c wait --for=condition=Ready pod/bystander --timeout=120s

verify: deploy samples
	./hack/verify-isolation.sh

verify-crd:
	./hack/verify-crd.sh

verify-crd-hardening:
	./hack/verify-crd-hardening.sh
