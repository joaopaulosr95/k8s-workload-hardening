TAG ?= dev
IMAGE ?= ghcr.io/joaopaulosr95/k8s-workload-hardening
CLUSTER ?= hardening

.PHONY: test cover image kind-up kind-down deploy \
        samples-isolation verify-isolation verify-crd-isolation \
        samples-hardening verify-hardening verify-crd-hardening \
        verify verify-crd

test:
	go test ./pkg/... -race

# AGENTS.md asks for 90% per code unit, not 90% overall — and the two differ:
# a package can rot to 70 while a 100%-covered sibling holds the total above
# the line. So the gate reads the per-package figures `go test` prints, not the
# profile's total, and fails when the run produced no coverage lines at all. A
# gate that cannot fail is worse than no gate, because it is believed.
cover:
	@go test ./pkg/... -coverprofile=coverage.out | tee /dev/stderr | awk '/coverage: [0-9.]+% of statements/ { seen = 1; for (i = 1; i <= NF; i++) if ($$i == "coverage:") { pct = $$(i + 1); sub(/%/, "", pct); if (pct + 0 < 90) { printf "%s: %s%% is below the 90%% per-package floor in AGENTS.md\n", $$2, pct; bad = 1 } } } /^FAIL/ { failed = 1 } END { if (!seen) { print "make cover: the test run produced no coverage lines"; exit 1 } exit (bad || failed) }'
	@go tool cover -func=coverage.out | tail -1

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

samples-isolation:
	kubectl apply -f deploy/samples/workloads.yaml
	kubectl -n tenant-a wait --for=condition=Ready pod/gateway --timeout=120s
	kubectl -n tenant-b wait --for=condition=Ready pod/dashboard --timeout=120s
	kubectl -n tenant-c wait --for=condition=Ready pod/bystander --timeout=120s

verify-isolation: deploy samples-isolation
	./hack/verify-isolation.sh

verify-crd-isolation:
	./hack/verify-crd-isolation.sh

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

verify-crd-hardening:
	./hack/verify-crd-hardening.sh

# Both features in one invocation. make runs `deploy` once however many targets
# name it, so the image is built and loaded a single time — which is what the
# CI job in specs/003-bonus/spec.md needs.
verify: verify-isolation verify-hardening

verify-crd: verify-crd-isolation verify-crd-hardening
