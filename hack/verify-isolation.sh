#!/usr/bin/env bash
# AC-07: on a live kind cluster, TCP and UDP traffic between the two groups —
# over pod IP and ClusterIP — works before isolation, fails after enforcement
# converges, and works again after the object is deleted. DNS and traffic to an
# unrelated pod are unaffected throughout.
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

iso=deploy/samples/isolation.yaml
converge_timeout=${CONVERGE_TIMEOUT:-60}
fail=0

# probe <src-ns> <src-pod> <proto> <host> — 0 if the reply came back.
probe() {
  local ns=$1 pod=$2 proto=$3 host=$4 out
  if [ "$proto" = tcp ]; then
    out=$(kubectl exec -n "$ns" "$pod" -- sh -c "wget -q -T 3 -O - http://$host:8080/ 2>/dev/null" 2>/dev/null || true)
  else
    out=$(kubectl exec -n "$ns" "$pod" -- sh -c "echo probe | nc -u -w 3 $host 8081 2>/dev/null" 2>/dev/null || true)
  fi
  [ -n "$out" ]
}

# pod_ready <namespace> <pod> - 0 if the kubelet still considers it Ready.
pod_ready() {
  [ "$(kubectl -n "$1" get pod "$2" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = "True" ]
}

check() {
  local want=$1 desc=$2; shift 2
  if "$@"; then got=reachable; else got=blocked; fi
  if [ "$got" = "$want" ]; then
    echo "ok    $desc: $got"
  else
    echo "FAIL  $desc: $got, want $want"
    fail=1
  fi
}

# wait_for <reachable|blocked> <probe args...> — poll until enforcement converges.
wait_for() {
  local want=$1; shift
  local deadline=$((SECONDS + converge_timeout))
  while [ $SECONDS -lt $deadline ]; do
    if "$@"; then got=reachable; else got=blocked; fi
    [ "$got" = "$want" ] && return 0
    sleep 2
  done
  return 1
}

gwIP=$(kubectl -n tenant-a get pod gateway -o jsonpath='{.status.podIP}')
dashIP=$(kubectl -n tenant-b get pod dashboard -o jsonpath='{.status.podIP}')
bystanderIP=$(kubectl -n tenant-c get pod bystander -o jsonpath='{.status.podIP}')
gwSvc=gateway.tenant-a.svc.cluster.local
dashSvc=dashboard.tenant-b.svc.cluster.local

echo "== before isolation =="
check reachable "A->B pod IP   TCP" probe tenant-a gateway   tcp "$dashIP"
check reachable "A->B pod IP   UDP" probe tenant-a gateway   udp "$dashIP"
check reachable "B->A pod IP   TCP" probe tenant-b dashboard tcp "$gwIP"
check reachable "B->A pod IP   UDP" probe tenant-b dashboard udp "$gwIP"
check reachable "A->B ClusterIP TCP" probe tenant-a gateway   tcp "$dashSvc"
check reachable "A->B ClusterIP UDP" probe tenant-a gateway   udp "$dashSvc"
check reachable "B->A ClusterIP TCP" probe tenant-b dashboard tcp "$gwSvc"
check reachable "B->A ClusterIP UDP" probe tenant-b dashboard udp "$gwSvc"
check reachable "A->bystander  TCP" probe tenant-a gateway   tcp "$bystanderIP"
check reachable "bystander->A  TCP" probe tenant-c bystander tcp "$gwIP"
check reachable "DNS from A"        kubectl exec -n tenant-a gateway -- nslookup kubernetes.default.svc.cluster.local
check reachable "kubelet probe keeps A ready" pod_ready tenant-a gateway

echo
echo "== applying isolation =="
kubectl apply -f "$iso" >/dev/null
kubectl -n isolation-system wait --for=jsonpath='{.status.phase}'=Active netiso/gateway-dashboard --timeout=60s
if ! wait_for blocked probe tenant-a gateway tcp "$dashIP"; then
  echo "FAIL  enforcement did not converge within ${converge_timeout}s"
  fail=1
fi

echo "== after isolation =="
check blocked   "A->B pod IP   TCP" probe tenant-a gateway   tcp "$dashIP"
check blocked   "A->B pod IP   UDP" probe tenant-a gateway   udp "$dashIP"
check blocked   "B->A pod IP   TCP" probe tenant-b dashboard tcp "$gwIP"
check blocked   "B->A pod IP   UDP" probe tenant-b dashboard udp "$gwIP"
check blocked   "A->B ClusterIP TCP" probe tenant-a gateway   tcp "$dashSvc"
check blocked   "A->B ClusterIP UDP" probe tenant-a gateway   udp "$dashSvc"
check blocked   "B->A ClusterIP TCP" probe tenant-b dashboard tcp "$gwSvc"
check blocked   "B->A ClusterIP UDP" probe tenant-b dashboard udp "$gwSvc"
check reachable "A->bystander  TCP" probe tenant-a gateway   tcp "$bystanderIP"
check reachable "bystander->A  TCP" probe tenant-c bystander tcp "$gwIP"
check reachable "bystander->B  TCP" probe tenant-c bystander tcp "$dashIP"
check reachable "DNS from A"        kubectl exec -n tenant-a gateway -- nslookup kubernetes.default.svc.cluster.local
check reachable "DNS from B"        kubectl exec -n tenant-b dashboard -- nslookup kubernetes.default.svc.cluster.local
# Kubelet probes originate from the node, and the policies allow pod sources
# only. If the CNI enforces ingress for non-pod sources, an isolated workload
# silently drops out of its Service - far worse than the block asked for.
check reachable "kubelet probe keeps A ready" pod_ready tenant-a gateway

echo
echo "== removing isolation =="
kubectl delete -f "$iso" --wait=true >/dev/null
if ! wait_for reachable probe tenant-a gateway tcp "$dashIP"; then
  echo "FAIL  connectivity did not return within ${converge_timeout}s"
  fail=1
fi

echo "== after removal =="
check reachable "A->B pod IP   TCP" probe tenant-a gateway   tcp "$dashIP"
check reachable "A->B pod IP   UDP" probe tenant-a gateway   udp "$dashIP"
check reachable "B->A pod IP   TCP" probe tenant-b dashboard tcp "$gwIP"
check reachable "B->A pod IP   UDP" probe tenant-b dashboard udp "$gwIP"
check reachable "A->B ClusterIP TCP" probe tenant-a gateway   tcp "$dashSvc"
check reachable "A->B ClusterIP UDP" probe tenant-a gateway   udp "$dashSvc"
check reachable "kubelet probe keeps A ready" pod_ready tenant-a gateway

left=$(kubectl get networkpolicies -A -l hardening.acme.corp/operation -o name | wc -l | tr -d ' ')
if [ "$left" != "0" ]; then
  echo "FAIL  $left owned policies survived deletion"
  fail=1
else
  echo "ok    no owned policies remain"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "AC-07 FAILED"
  exit 1
fi
echo "AC-07 PASSED"
