#!/usr/bin/env bash
# AC-09: the CRD installs and the API server rejects the shapes the controller
# should never have to handle.
set -euo pipefail

ns=crd-validation-test
fail=0

cleanup() { kubectl delete namespace "$ns" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

# expect_reject <description> <<<manifest
expect_reject() {
  local what=$1
  if kubectl apply -f - >/dev/null 2>&1; then
    echo "FAIL  accepted: $what"
    fail=1
  else
    echo "ok    rejected: $what"
  fi
}

kubectl apply -f deploy/crd.yaml >/dev/null
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

expect_reject "object missing group b" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: missing-b, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
EOF

expect_reject "matchExpressions supplied" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: with-expressions, namespace: $ns}
spec:
  a:
    namespace: tenant-a
    podSelector:
      matchLabels: {app: gateway}
      matchExpressions: [{key: app, operator: In, values: [gateway]}]
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

expect_reject "empty matchLabels" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: empty-labels, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

expect_reject "invalid namespace name" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: bad-namespace, namespace: $ns}
spec:
  a: {namespace: Tenant_A, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

# A prefixed label key is legal and must be accepted (Review Focus 4).
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {example.com/tier: gold}}}
EOF
echo "ok    accepted: valid object with a prefixed label key"

expect_reject "edit to an immutable spec" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: retargeted}}}
EOF

# Re-applying the identical spec must still be allowed: self == oldSelf holds.
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {example.com/tier: gold}}}
EOF
echo "ok    accepted: re-apply of an unchanged spec"

if [ "$fail" -ne 0 ]; then
  echo "AC-09 FAILED"
  exit 1
fi
echo "AC-09 PASSED"
