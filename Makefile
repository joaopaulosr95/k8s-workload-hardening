TAG ?= dev
IMAGE ?= ghcr.io/joaopaulosr95/k8s-workload-hardening
CLUSTER ?= hardening

.PHONY: test cover image kind-up kind-down deploy samples verify verify-crd verify-crd-hardening samples-hardening verify-hardening

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
	kubectl apply -f deploy/crd-hardening.yaml
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

samples-hardening:
	# From a clean slate: the tool's own annotations and patches survive a
	# re-apply, and verify-hardening asserts that a preview has written
	# nothing yet, so a second run would read the first run's results.
	kubectl delete namespace harden-a harden-b --ignore-not-found --wait
	kubectl apply -f deploy/samples/workloads-hardening.yaml
	kubectl -n harden-a rollout status deployment/fill-me --timeout=120s
	kubectl -n harden-a rollout status deployment/nonroot --timeout=120s
	kubectl -n harden-a rollout status deployment/already-hardened --timeout=120s
	kubectl -n harden-a rollout status deployment/skip-me --timeout=120s
	kubectl -n harden-b rollout status deployment/covered --timeout=120s

verify-hardening: deploy samples-hardening
	./hack/verify-hardening.sh
