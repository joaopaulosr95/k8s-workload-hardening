# How-to

Four goals, commands first. Each section says when it is the right move, gives the commands in
full, and says when it is the wrong one. For why any rule is the way it is, follow the link at
the end of the section — the reasoning lives in `specs/`, not here.

Every command assumes the controller is deployed (`make deploy`) and that `kubectl` is pointed
at the right cluster. The Makefile pins its own context; these are ad-hoc, so pin yours.

## Exempt a workload from hardening

Right when a workload must keep the configuration it has — a container that needs to run as
root, a request somebody sized by hand — and the exemption should outlive every future
`WorkloadHardening` object.

```console
$ kubectl -n tenant-a annotate deployment/legacy hardening.acme.corp/skip=true

$ kubectl -n tenant-a get deployment/legacy \
    -o jsonpath='{.metadata.annotations.hardening\.acme\.corp/skip}{"\n"}'
true
```

The annotation is a human's, and nothing in this tool writes or removes it. Every preview from
now on reports the workload as skipped instead of planning it.

```console
$ kubectl -n isolation-system get workloadhardening/tenant-hardening \
    -o jsonpath='{range .status.findings[*]}{.namespace}/{.name}: {.reason}{"\n"}{end}'
```

Wrong if the workload only needs *different* values. The policy is per-object, so create a
second `WorkloadHardening` over that namespace with the requests you want instead — an
exemption gives it nothing.

Rule: [BR-04 and AC-12](../specs/002-workload-hardening/spec.md).

## Approve part of a plan

Right when the preview is correct for some targets and not others, and you want the correct
ones rolled out now rather than after the argument about the rest.

```console
$ kubectl -n isolation-system get workloadhardening/tenant-hardening \
    -o jsonpath='{range .status.plan[*]}{.namespace}/{.kind}/{.name}  {.hash}  {.pods} pods{"\n"}{end}'
harden-a/Deployment/fill-me  fae72af1e7b9  2 pods
harden-a/Deployment/nonroot  112ef34e1963  2 pods
harden-b/Deployment/covered  bb5c37a56bae  1 pods

$ kubectl -n isolation-system patch workloadhardening/tenant-hardening --type=merge \
    -p '{"spec":{"approvedPlan":["fae72af1e7b9"]}}'

$ kubectl -n isolation-system get workloadhardening/tenant-hardening \
    -o jsonpath='{.status.phase}{"\n"}'
PartiallyApplied
```

`PartiallyApplied` is the correct end state here, not a failure: the targets you did not
approve are reported `Unapproved` and nothing was written to them. Add hashes to the same list
to roll out more; the object stays terminal between edits.

Wrong if every target is wanted. Approving all of them in one edit is one rollout window
rather than several.

Rule: [BR-07 and FR-01](../specs/002-workload-hardening/spec.md).

## Widen the blast radius deliberately

Right when the same policy should cover namespaces the original object did not name.
`spec.namespaces` is immutable, so this is a new object rather than an edit.

```console
$ kubectl -n isolation-system get workloadhardening/tenant-hardening -o yaml > /tmp/wider.yaml

$ kubectl -n isolation-system apply -f - <<'EOF'
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: tenant-hardening-wider, namespace: isolation-system}
spec:
  namespaces: [harden-c, harden-d]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  approvedPlan: []
EOF
```

Editing `spec.namespaces` on the original is refused by the CRD's transition rule, with the
message `only spec.approvedPlan may be changed`. That refusal is the design, not an obstacle:
the approved hashes describe the targets that were previewed, and a wider object would carry
approvals for a set nobody looked at.

Wrong if the new namespaces need different request values — that is a second object either
way, so there is nothing to weigh.

Rule: [BR-07](../specs/002-workload-hardening/spec.md).

## Read a `Stale` row

Right when a target you approved reports `Stale`: the workload changed after you read the plan,
so the hash you approved no longer describes what would be written.

```console
$ kubectl -n isolation-system get workloadhardening/tenant-hardening \
    -o jsonpath='{range .status.plan[*]}{.name}  outcome={.outcome}  approved={.approvedHash}  now={.hash}{"\n"}{end}'
fill-me  outcome=Stale  approved=fae72af1e7b9  now=9c1d0a4b7e52

$ kubectl -n isolation-system patch workloadhardening/tenant-hardening --type=merge \
    -p '{"spec":{"approvedPlan":["9c1d0a4b7e52"]}}'
```

The row keeps `approvedHash` so the attribution survives later resyncs. Re-approving the
current hash is an ordinary approval: read the new plan first, because the change that made it
stale may be one you did not want.

Wrong if several targets are `Stale` at once. Something is editing the workloads; find it
before re-approving, or the next approval is stale too.

Rule: [BR-07](../specs/002-workload-hardening/spec.md).
