# Tutorial

One path, from an empty machine to a hardened workload whose pods are Ready. About fifteen
minutes, most of it waiting for a cluster and an image build.

There are no choices in this document. Every branch, every alternative and every "if you
prefer" is in the [how-to](how-to.md); a tutorial that offers them stops being one. If a
command here does not produce what it says it produces, that is a bug in this document.

You need `docker`, `kind`, `kubectl`, `go` and `make`. Every `kubectl` below is pinned to the
cluster this tutorial creates.

## 1. A cluster

```console
$ make kind-up
```

A four-node kind cluster named `hardening`. The Makefile derives its context from that name, so
every `make` target from here on talks to this cluster and no other.

## 2. The controllers

```console
$ make deploy
```

Builds the image, loads it into the cluster, and applies both CRDs, the RBAC and the Deployment.
One process serves both custom resources.

```console
$ kubectl --context=kind-hardening -n isolation-system get deploy
NAME                     READY   UP-TO-DATE   AVAILABLE   AGE
k8s-workload-hardening   1/1     1            1           30s
```

## 3. Two namespaces that can talk to each other

```console
$ make samples-isolation
```

Three namespaces: `tenant-a` with a `gateway` pod, `tenant-b` with a `dashboard`, and
`tenant-c` with a `bystander` that exists to prove the isolation is not cluster-wide. Right
now all three can reach each other.

## 4. Isolate two of them

```console
$ kubectl --context=kind-hardening apply -f deploy/samples/isolation.yaml

$ kubectl --context=kind-hardening -n isolation-system wait \
    --for=jsonpath='{.status.phase}'=Active networkisolation/gateway-dashboard --timeout=90s
networkisolation.hardening.acme.corp/gateway-dashboard condition met
```

Two NetworkPolicies now exist, one in each namespace, and `gateway` and `dashboard` can no
longer reach each other in either direction. `bystander` is untouched — it was never named.

```console
$ kubectl --context=kind-hardening -n tenant-a get networkpolicy
```

`make verify` proves the packets really stop, over TCP and UDP, by pod IP and ClusterIP, in
both directions. That is a longer run than this tutorial; the policies above are what it
checks.

## 5. A workload with gaps

```console
$ make samples-hardening
```

Five Deployments across `harden-a` and `harden-b`. `fill-me` has no resource requests and a
container that explicitly runs as root; the others exist to show what the tool refuses to
touch.

## 6. Preview — the step the whole design exists for

```console
$ kubectl --context=kind-hardening apply -f deploy/samples/hardening.yaml

$ kubectl --context=kind-hardening -n isolation-system wait \
    --for=jsonpath='{.status.phase}'=Previewed workloadhardening/tenant-hardening --timeout=90s
workloadhardening.hardening.acme.corp/tenant-hardening condition met
```

Nothing has been written to any workload. Read what would be:

```console
$ kubectl --context=kind-hardening -n isolation-system get workloadhardening/tenant-hardening \
    -o jsonpath='{range .status.plan[*]}{.namespace}/{.name}  {.hash}  {.pods} pods{"\n"}{end}'
harden-a/fill-me  fae72af1e7b9  2 pods
harden-a/nonroot  112ef34e1963  2 pods
harden-b/covered  bb5c37a56bae  1 pods
```

Each row is one target, the change hash that describes exactly what would be written to it, and
how many pods restart if you approve it. Targets the tool declines are reported separately:

```console
$ kubectl --context=kind-hardening -n isolation-system get workloadhardening/tenant-hardening \
    -o jsonpath='{range .status.findings[*]}{.namespace}/{.name}: {.reason}{"\n"}{end}'
```

## 7. Approve it

Approval is per target, by hash. Copy every hash from the plan into `spec.approvedPlan`:

```console
$ hashes=$(kubectl --context=kind-hardening -n isolation-system \
    get workloadhardening/tenant-hardening \
    -o jsonpath='{range .status.plan[*]}{.hash}{"\n"}{end}' | sed 's/.*/"&"/' | paste -sd, -)

$ kubectl --context=kind-hardening -n isolation-system patch \
    workloadhardening/tenant-hardening --type=merge \
    -p "{\"spec\":{\"approvedPlan\":[$hashes]}}"

$ kubectl --context=kind-hardening -n isolation-system wait \
    --for=jsonpath='{.status.phase}'=Applied workloadhardening/tenant-hardening --timeout=120s
workloadhardening.hardening.acme.corp/tenant-hardening condition met
```

Now look at what changed, and at the record it left beside it:

```console
$ kubectl --context=kind-hardening -n harden-a get deployment/fill-me \
    -o jsonpath='{.spec.template.spec.containers[0].resources}{"\n"}'
{"requests":{"cpu":"10m","memory":"32Mi"}}

$ kubectl --context=kind-hardening -n harden-a get deployment/fill-me \
    -o jsonpath='{.metadata.annotations.hardening\.acme\.corp/filled}{"\n"}'
```

The annotation names every leaf path written and the value written to it. It outlives this
object: delete the `WorkloadHardening` and the record stays on the workload.

## 8. The pods come back

```console
$ kubectl --context=kind-hardening -n harden-a rollout status deployment/fill-me --timeout=180s
deployment "fill-me" successfully rolled out
```

This is the claim the whole tutorial is making. A template patch restarts every pod of every
target, and a hardening that leaves them unschedulable has made things worse, not better.
`fill-me`'s container declares `runAsUser: 0`, so the tool did **not** write pod-level
`runAsNonRoot` — that evidence is why these pods are Ready instead of stuck in
`CreateContainerConfigError`.

## 9. What it is doing

```console
$ kubectl --context=kind-hardening -n isolation-system run metrics-probe \
    --rm -i --restart=Never --image=curlimages/curl:8.11.1 --quiet -- \
    -sS http://k8s-workload-hardening-metrics:8080/metrics | grep '^hardening_'
```

Five series: reconciles by resource and phase, targets patched, dry-run refusals, apply
failures, and queue depth.

## 10. Tear it down

```console
$ make kind-down
```

---

Next: the [how-to](how-to.md) for the branches this document refused to show, the
[reference](reference.md) for the vocabulary, and [specs/](../specs/) for why any of it is the
way it is.
