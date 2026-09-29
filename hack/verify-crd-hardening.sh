#!/usr/bin/env bash
# AC-16: the CRD installs and the API server rejects an empty namespace list, a
# missing resources.requests, a resources.limits key, and an edit to any field
# other than approvedPlan.
set -euo pipefail

# Every kubectl call below is pinned to one context. Unpinned, this script runs
# against whatever kubeconfig happens to be selected — and it deletes namespaces
# and patches live workloads, so a stale context turns a verification run into an
# incident. Override with KUBECONTEXT=... for a cluster under another name.
CONTEXT="${KUBECONTEXT:-kind-hardening}"
kubectl() { command kubectl --context="$CONTEXT" "$@"; }

if ! command kubectl config get-contexts "$CONTEXT" >/dev/null 2>&1; then
  echo "no kubecontext named '$CONTEXT'; set KUBECONTEXT or run 'make kind-up'" >&2
  exit 1
fi

ns=hardening-crd-test
fail=0

cleanup() { kubectl delete namespace "$ns" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

# expect_reject <description> <expected-error-substring> <<<manifest
#
# The expected substring is not decoration: without it a manifest that fails to
# parse, or one rejected for an unrelated reason, reads as a passing test.
expect_reject() {
  local what=$1 want=$2 out
  if out=$(kubectl apply -f - 2>&1); then
    echo "FAIL  accepted: $what"
    fail=1
  elif ! printf '%s' "$out" | grep -qF "$want"; then
    echo "FAIL  rejected for the wrong reason: $what"
    echo "        want substring: $want"
    echo "        got: $(printf '%s' "$out" | head -2 | tr '\n' ' ')"
    fail=1
  else
    echo "ok    rejected: $what"
  fi
}

expect_accept() {
  local what=$1 out
  if out=$(kubectl apply -f - 2>&1); then
    echo "ok    accepted: $what"
  else
    echo "FAIL  rejected: $what"
    echo "        got: $(printf '%s' "$out" | head -2 | tr '\n' ' ')"
    fail=1
  fi
}

kubectl apply -f deploy/crd-hardening.yaml >/dev/null
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

expect_reject "empty namespace list" "should have at least 1 items" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: no-namespaces, namespace: $ns}
spec:
  namespaces: []
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

expect_reject "seventeen namespaces" "must have at most 16 items" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: too-many, namespace: $ns}
spec:
  namespaces: [n01, n02, n03, n04, n05, n06, n07, n08, n09, n10, n11, n12, n13, n14, n15, n16, n17]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

expect_reject "duplicate namespace" "Duplicate value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: duplicate-ns, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-a]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

expect_reject "resources omitted entirely" "spec.resources: Required value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: no-resources, namespace: $ns}
spec:
  namespaces: [tenant-a]
EOF

expect_reject "requests omitted" "spec.resources.requests: Required value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: no-requests, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {}
EOF

expect_reject "requests naming only cpu" "spec.resources.requests.memory: Required value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: cpu-only, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {requests: {cpu: 10m}}
EOF

expect_reject "a resources.limits key" "limits: Too many" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: with-limits, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources:
    requests: {cpu: 10m, memory: 32Mi}
    limits: {cpu: 500m, memory: 1Gi}
EOF

# maxProperties: 0 rejects any key inside limits but admits an empty object, so
# the CEL rule is what closes it. Without both, "limits are never written" is
# a claim the schema only half enforces.
expect_reject "an empty resources.limits" "limits is not supported" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: { name: empty-limits, namespace: $ns }
spec:
  namespaces: [tenant-a]
  resources:
    requests: { cpu: 10m, memory: 32Mi }
    limits: {}
EOF

# Review Focus 6, at the API-server level: the loose regular expression that
# ParseQuantity quotes in its error message accepts "10mm", and the Go
# conversion then cannot read it. The tight grammar in the schema must refuse
# it here, before it is ever stored.
expect_reject "a quantity with a two-character suffix" "spec.resources.requests.cpu in body should match" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: bad-quantity, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {requests: {cpu: 10mm, memory: 32Mi}}
EOF

expect_reject "a thirteen-character hash" "approvedPlan[0] in body should match" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: bad-hash, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  approvedPlan: [0123456789abc]
EOF

expect_accept "a valid unarmed object" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

# approvedPlan is the only mutable field: arming an existing object is the
# entire workflow and must be accepted (BR-07, FR-01).
expect_accept "arming the object by adding approvedPlan" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

expect_reject "an edit to spec.namespaces" "only spec.approvedPlan may be changed" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-c]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

expect_reject "an edit to spec.resources.requests" "only spec.approvedPlan may be changed" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 500m, memory: 32Mi}}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

expect_reject "an edit to spec.securityContext" "only spec.approvedPlan may be changed" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  securityContext: {readOnlyRootFilesystem: true}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

if [ "$fail" -ne 0 ]; then
  echo "AC-16 FAILED"
  exit 1
fi
echo "AC-16 PASSED"
