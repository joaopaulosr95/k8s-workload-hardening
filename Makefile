TAG ?= dev
IMAGE ?= ghcr.io/joaopaulosr95/k8s-workload-hardening
CLUSTER ?= hardening
# Every kubectl call below is pinned to one context. Unpinned, `make verify`
# runs against whatever kubeconfig happens to be selected — and it deletes
# namespaces, patches live workloads and restarts a Deployment. Override for a
# cluster under another name.
KUBECONTEXT ?= kind-$(CLUSTER)
KUBECTL = kubectl --context=$(KUBECONTEXT)

.PHONY: test cover envtest-assets test-envtest image kind-up kind-down deploy \
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

# Pinned, both of them. setup-envtest at HEAD would change the control-plane
# version under CI without a commit, and a schema that passes on one API server
# version and fails on the next is exactly what this suite exists to catch.
# Both pins track what the code compiles against: controller-runtime v0.25.x
# and k8s.io/* v0.37.1, so the control plane is the one the client libraries
# target rather than four minors behind it.
ENVTEST_VERSION ?= release-0.25
ENVTEST_K8S_VERSION ?= 1.37.0
SETUP_ENVTEST = go run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

envtest-assets:
	@$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(CURDIR)/bin -p path

# GOTESTFLAGS lets CI ask for -v. It needs the per-test lines: `go test`
# suppresses a passing package's own output, so the suite's "assets unset"
# message never reaches a grep while the ok line does -- a skipped suite and a
# real one are the same two lines without it.
GOTESTFLAGS ?=

test-envtest:
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(CURDIR)/bin -p path)" \
		go test ./test/envtest/... -count=1 $(GOTESTFLAGS)

image:
	docker build -t $(IMAGE):$(TAG) .

kind-up:
	kind create cluster --name $(CLUSTER) --config hack/kind/cluster.yaml

kind-down:
	kind delete cluster --name $(CLUSTER)

deploy: image
	kind load docker-image $(IMAGE):$(TAG) --name $(CLUSTER)
	$(KUBECTL) apply -f deploy/crd-isolation.yaml
	$(KUBECTL) apply -f deploy/crd-hardening.yaml
	$(KUBECTL) apply -f deploy/rbac.yaml
	$(KUBECTL) apply -f deploy/controller.yaml
	$(KUBECTL) apply -f deploy/metrics-service.yaml
	$(KUBECTL) -n isolation-system rollout restart deployment/network-isolation
	$(KUBECTL) -n isolation-system rollout status deployment/network-isolation --timeout=120s

samples-isolation:
	$(KUBECTL) apply -f deploy/samples/workloads.yaml
	$(KUBECTL) -n tenant-a wait --for=condition=Ready pod/gateway --timeout=120s
	$(KUBECTL) -n tenant-b wait --for=condition=Ready pod/dashboard --timeout=120s
	$(KUBECTL) -n tenant-c wait --for=condition=Ready pod/bystander --timeout=120s

verify-isolation: deploy samples-isolation
	./hack/verify-isolation.sh

verify-crd-isolation:
	./hack/verify-crd-isolation.sh

samples-hardening:
	# From a clean slate: the tool's own annotations and patches survive a
	# re-apply, and verify-hardening asserts that a preview has written
	# nothing yet, so a second run would read the first run's results.
	$(KUBECTL) delete namespace harden-a harden-b --ignore-not-found --wait
	$(KUBECTL) apply -f deploy/samples/workloads-hardening.yaml
	$(KUBECTL) -n harden-a rollout status deployment/fill-me --timeout=120s
	$(KUBECTL) -n harden-a rollout status deployment/nonroot --timeout=120s
	$(KUBECTL) -n harden-a rollout status deployment/already-hardened --timeout=120s
	$(KUBECTL) -n harden-a rollout status deployment/skip-me --timeout=120s
	$(KUBECTL) -n harden-b rollout status deployment/covered --timeout=120s

verify-hardening: deploy samples-hardening
	./hack/verify-hardening.sh

verify-crd-hardening:
	./hack/verify-crd-hardening.sh

# Both features in one invocation. make runs `deploy` once however many targets
# name it, so the image is built and loaded a single time — which is what the
# CI job in specs/003-bonus/spec.md needs.
verify: verify-isolation verify-hardening

verify-crd: verify-crd-isolation verify-crd-hardening
