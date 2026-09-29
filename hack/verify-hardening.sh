#!/usr/bin/env bash
# AC-15, on a live kind cluster: a Deployment with no requests and a root
# container is previewed, approved by hash, applied, rolls out and stays Ready;
# a Deployment declaring both cpu and memory limits and no requests is not
# patched and keeps QoS Guaranteed; a namespace whose LimitRange sets only
# `default` reports its gaps as covered; a skip-annotated Deployment is
# untouched; and the provenance annotation is correct.
set -euo pipefail

request=deploy/samples/hardening.yaml
ns=isolation-system
name=tenant-hardening
fail=0

ok()   { echo "ok    $1"; }
bad()  { echo "FAIL  $1"; fail=1; }

# check <description> <expected> <actual>
check() {
  if [ "$2" = "$3" ]; then ok "$1: $3"; else bad "$1: got '$3', want '$2'"; fi
}

# contains <description> <haystack> <needle>
contains() {
  if printf '%s' "$2" | grep -qF -- "$3"; then ok "$1"; else bad "$1 (missing '$3')"; fi
}

# absent <description> <haystack> <needle>
absent() {
  if printf '%s' "$2" | grep -qF -- "$3"; then bad "$1 (found '$3')"; else ok "$1"; fi
}

filled() { kubectl -n "$1" get deploy "$2" -o jsonpath='{.metadata.annotations.hardening\.acme\.corp/filled}' 2>/dev/null || true; }
tmpl()   { kubectl -n "$1" get deploy "$2" -o jsonpath="{.spec.template.spec$3}" 2>/dev/null || true; }
qos()    { kubectl -n "$1" get pod -l "app=$2" -o jsonpath='{.items[0].status.qosClass}' 2>/dev/null || true; }

echo "== preview =="
kubectl apply -f "$request" >/dev/null
kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=Previewed "workloadhardening/$name" --timeout=90s >/dev/null

plan=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{range .status.plan[*]}{.namespace}/{.kind}/{.name} {.hash}{"\n"}{end}')
findings=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{range .status.findings[*]}{.namespace}/{.name}/{.container}: {.reason}{"\n"}{end}')
echo "$plan"

# Nothing may have been written yet: a preview writes nothing (AC-08).
untouched=0
for d in harden-a/fill-me harden-a/nonroot harden-a/already-hardened harden-a/skip-me harden-b/covered; do
  if [ -n "$(filled "${d%%/*}" "${d##*/}")" ]; then
    bad "preview wrote a provenance annotation onto $d"
    untouched=1
  fi
done
[ "$untouched" -eq 0 ] && ok "preview wrote nothing"

# The plan must name the three targets with gaps and neither of the two without.
contains "fill-me is planned"          "$plan" "harden-a/Deployment/fill-me"
contains "nonroot is planned"          "$plan" "harden-a/Deployment/nonroot"
contains "covered is planned"          "$plan" "harden-b/Deployment/covered"
absent   "already-hardened has no gaps" "$plan" "already-hardened"
absent   "skip-me is not a target"      "$plan" "skip-me"

# BR-01's middle row, and BR-06's, reported rather than acted on.
contains "the defaulted-from-limit finding is reported" "$findings" "defaulted from limit"
contains "the LimitRange coverage finding is reported"  "$findings" "covered by LimitRange"
contains "the skip annotation is reported"              "$findings" "hardening.acme.corp/skip"

echo
echo "== approve by hash =="
# grep exits 1 on no match, which under `set -e` would abort here with no
# message at all — an empty plan is a failure to report, not a reason to stop.
hashes=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{range .status.plan[*]}{.hash}{"\n"}{end}' | grep -v '^$' || true)
if [ -z "$hashes" ]; then
  bad "the plan published no hashes, so there is nothing to approve"
  echo "AC-15 FAILED"
  exit 1
fi
list=$(printf '%s' "$hashes" | sed 's/.*/"&"/' | paste -sd, -)
kubectl -n "$ns" patch workloadhardening "$name" --type=merge -p "{\"spec\":{\"approvedPlan\":[$list]}}" >/dev/null
kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=Applied "workloadhardening/$name" --timeout=120s >/dev/null
ok "phase reached Applied"

echo
echo "== the rollout completes and the pods stay Ready =="
kubectl -n harden-a rollout status deployment/fill-me --timeout=180s >/dev/null && ok "fill-me rolled out" || bad "fill-me did not roll out"
kubectl -n harden-a rollout status deployment/nonroot --timeout=180s >/dev/null && ok "nonroot rolled out" || bad "nonroot did not roll out"
kubectl -n harden-b rollout status deployment/covered --timeout=180s >/dev/null && ok "covered rolled out" || bad "covered did not roll out"

echo
echo "== AC-05: root evidence suppressed pod-level runAsNonRoot =="
# fill-me's container declares runAsUser: 0, so writing runAsNonRoot: true
# would produce CreateContainerConfigError and the pods above would never have
# become Ready. This is the assertion the rollout above already proved; making
# it explicit says why.
sc=$(tmpl harden-a fill-me '.securityContext')
absent   "fill-me has no pod-level runAsNonRoot" "$sc" "runAsNonRoot"
contains "fill-me got the seccomp profile"       "$sc" "RuntimeDefault"
csc=$(tmpl harden-a fill-me '.containers[0].securityContext')
contains "fill-me got allowPrivilegeEscalation"  "$csc" "allowPrivilegeEscalation"
contains "fill-me got capabilities.drop"         "$csc" "ALL"

# nonroot evidences nothing, so it does get the field, and it is still Ready.
contains "nonroot got pod-level runAsNonRoot" "$(tmpl harden-a nonroot '.securityContext')" "runAsNonRoot"

echo
echo "== AC-03: a limits-only Deployment is untouched and stays Guaranteed =="
check "already-hardened has no provenance annotation" "" "$(filled harden-a already-hardened)"
absent "already-hardened has no requests written" "$(tmpl harden-a already-hardened '.containers[0].resources')" "requests"
check "already-hardened QoS" "Guaranteed" "$(qos harden-a already-hardened)"

echo
echo "== AC-06: a LimitRange with only 'default' covers the requests =="
check "covered was patched" "true" "$([ -n "$(filled harden-b covered)" ] && echo true || echo false)"
absent "covered got no resource requests" "$(filled harden-b covered)" "resources.requests"
contains "covered got its securityContext" "$(filled harden-b covered)" "allowPrivilegeEscalation"

echo
echo "== AC-12: the skip annotation is honoured =="
check "skip-me has no provenance annotation" "" "$(filled harden-a skip-me)"
# The API server defaults spec.template.spec.securityContext to {} on every
# Deployment, so {} is the untouched state and "" never occurs. What separates
# patched from untouched is whether any policy field is inside it, and whether
# the container gained a securityContext at all.
check "skip-me has an empty pod securityContext" "{}" "$(tmpl harden-a skip-me '.securityContext')"
check "skip-me has no container securityContext" "" "$(tmpl harden-a skip-me '.containers[0].securityContext')"
# resources is a struct rather than a pointer, so it serialises as {} when
# nothing was written into it, for the same reason.
check "skip-me has empty container resources" "{}" "$(tmpl harden-a skip-me '.containers[0].resources')"

echo
echo "== AC-11: the provenance annotation records leaf paths and values =="
prov=$(filled harden-a fill-me)
printf '%s\n' "$prov"
for want in \
  "spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault" \
  "spec.template.spec.containers[probe].securityContext.allowPrivilegeEscalation=false" \
  "spec.template.spec.containers[probe].securityContext.capabilities.drop=[ALL]" \
  "spec.template.spec.containers[probe].resources.requests.cpu=10m" \
  "spec.template.spec.containers[probe].resources.requests.memory=32Mi"; do
  contains "provenance records $want" "$prov" "$want"
done
absent "provenance never records runAsNonRoot for fill-me" "$prov" "runAsNonRoot"
absent "provenance never records a limit"                  "$prov" "limits"

echo
echo "== the applied object is terminal =="
before=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{.status.lastReconcileTime}')
sleep 35   # longer than the controller's 30s resync
after=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{.status.lastReconcileTime}')
check "an Applied object is not rewritten on resync" "$before" "$after"

echo
if [ "$fail" -ne 0 ]; then
  echo "AC-15 FAILED"
  exit 1
fi
echo "AC-15 PASSED"
