#!/usr/bin/env bash
# AC-09: the CRD installs and the API server rejects the shapes the controller
# should never have to handle.
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

ns=crd-validation-test
fail=0

cleanup() { kubectl delete namespace "$ns" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

# expect_reject <description> <expected-error-substring> <<<manifest
#
# The expected substring is not decoration: without it a manifest that fails to
# parse, or one rejected for an unrelated reason, reads as a passing test. The
# point is that the API server rejects it for the reason the schema encodes.
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

kubectl apply -f deploy/crd.yaml >/dev/null
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

expect_reject "only one peer supplied" "should have at least 2 items" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: one-peer, namespace: $ns}
spec:
  peers:
    - {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
EOF

expect_reject "three peers supplied" "Too many: 3: must have at most 2 items" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: three-peers, namespace: $ns}
spec:
  peers:
    - {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
    - {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
    - {namespace: tenant-c, podSelector: {matchLabels: {app: extra}}}
EOF

expect_reject "peers omitted entirely" "spec.peers: Required value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: no-peers, namespace: $ns}
spec: {}
EOF

expect_reject "matchExpressions supplied" "matchExpressions: Too many" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: with-expressions, namespace: $ns}
spec:
  peers:
    - namespace: tenant-a
      podSelector:
        matchLabels: {app: gateway}
        matchExpressions: [{key: app, operator: In, values: [gateway]}]
    - {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

expect_reject "empty matchLabels" "should have at least 1 properties" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: empty-labels, namespace: $ns}
spec:
  peers:
    - {namespace: tenant-a, podSelector: {matchLabels: {}}}
    - {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

expect_reject "invalid namespace name" "spec.peers[0].namespace in body should match" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: bad-namespace, namespace: $ns}
spec:
  peers:
    - {namespace: Tenant_A, podSelector: {matchLabels: {app: gateway}}}
    - {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

# A prefixed label key is legal and must be accepted (Review Focus 4).
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  peers:
    - {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
    - {namespace: tenant-b, podSelector: {matchLabels: {example.com/tier: gold}}}
EOF
echo "ok    accepted: valid object with a prefixed label key"

expect_reject "edit to an immutable spec" "spec is immutable" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  peers:
    - {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
    - {namespace: tenant-b, podSelector: {matchLabels: {app: retargeted}}}
EOF

# Re-applying the identical spec must still be allowed: self == oldSelf holds.
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  peers:
    - {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
    - {namespace: tenant-b, podSelector: {matchLabels: {example.com/tier: gold}}}
EOF
echo "ok    accepted: re-apply of an unchanged spec"

if [ "$fail" -ne 0 ]; then
  echo "AC-09 FAILED"
  exit 1
fi
echo "AC-09 PASSED"
