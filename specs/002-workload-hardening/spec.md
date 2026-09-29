---
name: On-demand workload hardening
description: |
  Fill in missing resource requests and missing securityContext hardening across one or
  more namespaces, through a WorkloadHardening custom resource. Every change is previewed
  and approved per target, by hash, before anything is written.
author: João Bastos <joaopaulosr95@gmail.com>
status: Approved
relatedResources:
  - specs/001-network-isolation/spec.md
  - specs/003-bonus/spec.md
---

# On-demand workload hardening

## Objective

Let an SRE fill in missing resource requests and missing securityContext hardening across one
or more namespaces, on demand, after showing exactly what would change — without breaking the
workloads being patched.

## Context

This covers core task 2 of the assignment. Core task 1, network isolation, is specified in
`specs/001-network-isolation/spec.md` and implemented.

This feature reuses 001's shape: a namespaced CRD, one controller process, a pure function
producing the desired change, status as the operator's view. It diverges deliberately in one
respect. Isolation is desired state — NetworkPolicies must persist while pods come and go, so
the object owns them, carries a finalizer, and repairs drift. Hardening is not. It mutates
objects other teams own and edit, and a field written into a workload's own template needs no
custodian. So there is no finalizer, no workload informer and no drift repair: the request runs
once, records what it did on the objects it touched, and stops. A continuous reconcile would
eventually overwrite a deliberate later change — someone raising a memory limit after an
OOMKill — and start a rollout to do it.

The risk is inverted relative to 001. Isolation's failure mode is not blocking traffic it was
asked to block. Hardening's failure mode is breaking a workload that was working. Two facts,
both verified against a live cluster rather than assumed, set the shape of everything below.

**The template is not what runs.** Kubernetes copies `limits` into `requests` when requests are
absent, and it does so when defaulting the **Pod** — never the workload template. A Deployment
whose template declares `limits: {cpu: 500m, memory: 1Gi}` and no requests produces pods with
`requests` equal to those limits and QoS class `Guaranteed`. A tool that reads templates sees an
absent field and calls it a gap; filling it with `{cpu: 10m, memory: 32Mi}` cuts the CPU
reservation 50×, the memory reservation 32×, and demotes the pod to `Burstable` — the exact harm
this feature exists to avoid, performed by the feature itself. Hence BR-01's effective-value
rule, which applies to a namespace LimitRange for the same reason (BR-06).

**The dry-run is not a safety net for securityContext.** Every root-related failure is enforced
by the kubelet or the kernel, not by API validation. The API server accepts `runAsNonRoot: true`
alongside `runAsUser: 0` on a server-side dry-run without complaint, and accepts a privileged
root container under a pod-level `runAsNonRoot: true`; the pod then fails with
`CreateContainerConfigError: container has runAsNonRoot and image will run as root`. The dry-run
of FR-03 catches schema, admission and webhook problems, and none of the failures BR-02 is
about. The only net for those is the rollout halting itself, and BR-02 states per kind how much
of a net that is.

## Terminology

- **Target:** a Deployment, StatefulSet or DaemonSet in a selected namespace that is not
  excluded by BR-04.
- **Template:** a target's `spec.template.spec`. This is the only thing read and the only thing
  patched. Running pods are never patched.
- **Effective value:** the value that actually applies to a container once Kubernetes' own
  defaulting has run. Three rules matter, and none is visible in the template:
  - securityContext fields resolve by pod → container precedence, the container winning;
  - a resource with `limits` set and `requests` absent has an effective request **equal to the
    limit**, applied when the Pod is defaulted;
  - a namespace LimitRange supplies both, and its `default` becomes the request when
    `defaultRequest` is omitted (BR-06).
- **Gap:** a field the policy would set whose **effective value** is absent. Not merely a field
  the template omits.
- **Plan:** every gap across every target, with the value that would be written into each, plus
  every finding that cannot be acted on.
- **Change hash:** the first 12 hex digits of SHA-256 over a canonical serialisation of one
  target's change. Per target, never per plan (BR-07).
- **Finding:** something observed and reported but not patched, with the reason.
- **Provenance annotation:** `hardening.acme.corp/filled`, written onto a patched target,
  recording the leaf paths written and the values written.

## Business rules

### BR-01 — Fill gaps only, judged by effective value

Write a field only where its **effective value** is absent. Never overwrite, never lower, never
correct a value that is already present. A container that explicitly declares
`runAsNonRoot: false`, or `cpu: 1m`, is reported as a finding and left alone.

Reading the template alone is not sufficient to decide this, and resources are the case that
bites:

| Container declares         | Template `requests` | Effective request | Gap?                     |
| -------------------------- | ------------------- | ----------------- | ------------------------ |
| nothing                    | absent              | none              | **yes**                  |
| `limits: {memory: 1Gi}`    | absent              | **1Gi**           | **no** — finding instead |
| `requests: {memory: 64Mi}` | `64Mi`              | 64Mi              | no                       |

Row 2 is reported as _"request defaulted from limit"_. Patching it would cut the pod's
reservation, which no status field would flag and no dry-run would refuse.

This rule carries the rest of the feature. It makes a re-run idempotent with no arithmetic; it
makes the inverse of every change the deletion of a field rather than the restoration of a
value, so what this tool did is always reversible by hand from the record it leaves (BR-08); and
it means the tool never overrules a decision someone made on purpose.

Consequence, accepted: the tool cannot harden a workload that is unhardened by explicit choice.
It reports it instead.

### BR-02 — What "hardened" means, and what actually bounds the damage

| Field                      | Level     | Value            | Written                  |
| -------------------------- | --------- | ---------------- | ------------------------ |
| `runAsNonRoot`             | pod       | `true`           | unless root is evidenced |
| `seccompProfile.type`      | pod       | `RuntimeDefault` | always                   |
| `allowPrivilegeEscalation` | container | `false`          | always                   |
| `capabilities.drop`        | container | `["ALL"]`        | always                   |
| `readOnlyRootFilesystem`   | container | `true`           | only when requested      |

"Container" means both `containers` and `initContainers`; names are unique across the two lists
within a pod. `ephemeralContainers` are never patched — they are added to running pods, not to
templates.

The first four are what the `restricted` Pod Security Standard requires. They do not all fail at
the same moment, and the difference is what the opt-in below is for:

- **`runAsNonRoot`** is enforced by the kubelet at container creation. The pod never starts.
- **The other three** are applied at creation and enforced by the kernel **when exercised** — a
  syscall the `RuntimeDefault` profile denies, a write needing `DAC_OVERRIDE` after
  `capabilities.drop: ALL`, a setuid binary exec'd under `no_new_privs`. Most images that break
  break immediately, at the entrypoint; some break later.

Where the failure is immediate, the rollout halts itself. That bound is not uniform across the
kinds this tool patches, and it is not "the old pods keep serving" except for one of them:

| Kind                              | Update mechanism (defaults; an operator can override)                   | If the patched pod never becomes Ready                                                                                                               |
| --------------------------------- | ----------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| Deployment                        | `maxSurge` 25% rounds **up**, `maxUnavailable` 25% rounds **down**      | New pods created first. At `replicas ≤ 3`, `maxUnavailable` is 0 and every old pod keeps serving; above that up to 25% are taken down and stay down. |
| StatefulSet                       | Reverse ordinal, one pod at a time, **terminate-then-create**, no surge | That pod is down until reverted. At `replicas: 1`, the workload is down.                                                                             |
| DaemonSet                         | `maxUnavailable: 1`, `maxSurge: 0`, **delete-then-create** per node     | That node loses the workload. The rollout halts after one node.                                                                                      |
| `paused`, `OnDelete`, `partition` | No rollout, or none for the partitioned ordinals                        | Nothing now; it breaks at the next eviction, drain or scale. Refused (BR-04).                                                                        |

So the honest claim is: the failure is **usually immediate, bounded to a fraction of one
workload, and halts its own rollout**. `readOnlyRootFilesystem` has none of those properties
reliably — a container that writes to its filesystem generally does so after it is serving, so
the failure arrives once every pod has already been replaced and nothing halts. It is not part
of restricted PSS either. Hence the opt-in, and hence BR-04 refusing the configurations where no
rollout happens at all.

Never written:

- **`runAsUser`.** A guessed UID starts the container and then breaks file ownership — the
  silent, late failure this rule exists to avoid. Relying on the image's own `USER` means
  `runAsNonRoot` fails loudly instead when the image runs as root, which is the outcome we want.
- **`privileged`.** A container explicitly privileged needs it, and dropping its capabilities
  would be theatre. Reported as a finding.
- **`runAsNonRoot`, for any pod where a container evidences a need for root.** Because this
  field is written at **pod** level, one such container poisons it for the entire pod. Evidence
  is an explicit `privileged: true`, or an effective `runAsUser` of 0, on any container in the
  pod. Reporting such a container as a finding while still writing pod-level `runAsNonRoot` is
  the single most likely way this tool breaks a DaemonSet, because CNI agents, log shippers and
  node exporters are routinely privileged without ever declaring `runAsUser: 0`.

### BR-03 — Resources: requests only

Absent **requests** are filled from the values named in the request object. **Limits are never
written.** An absent limit is a finding whose remedy names the mechanism that exists for it.

Filling requests moves a container out of the `BestEffort` QoS class, which is the first thing
evicted under node pressure. That is the benefit being bought, and it costs little: being wrong
about a request costs scheduling position and eviction priority, and it shows up in
`kubectl describe node` rather than in a pager.

Limits are a different class of change and this tool is the wrong instrument for them. A memory
limit that is too small kills the container after a rollout that completed green, with every
previous pod already gone. One number in one object, spread across up to 16 namespaces of
heterogeneous workloads, is guaranteed wrong for some of them, and D-07's reasoning does not
stop applying because a human typed the number rather than the tool.

The mechanism for limits is a **LimitRange**: per namespace, carrying `min`, `max` and
`maxLimitRequestRatio`, applied by the API server, and already modelled in BR-06. The
composition works in the operator's favour — add a LimitRange, then run this tool to fill
requests, and the rollout the patch triggers is what makes the existing pods pick the limits up.

### BR-04 — Selection and exclusion

An explicit list of 1–16 namespaces. Target kinds are **Deployment, StatefulSet and DaemonSet**:
all three carry the template at the same path, so all three are one code path.

Excluded, never read as a target:

- protected namespaces (BR-05);
- any object carrying a **controlling** `ownerReference` — the owner is the target instead, so a
  ReplicaSet owned by a Deployment is skipped and the Deployment is patched;
- any object annotated `hardening.acme.corp/skip: "true"`, the escape hatch for a workload that
  genuinely needs what the policy would take away.

**Refused, and reported:** any target with `spec.paused: true`, an update strategy of
`OnDelete`, or a StatefulSet `rollingUpdate.partition` above 0. None of these roll the patch out
to every pod, so it sits inert and the workload breaks at some arbitrary later moment — a node
drain, an eviction, a partition lowered days afterwards. That is the late, silent failure class
BR-02 uses to justify making `readOnlyRootFilesystem` opt-in, so consistency requires refusing
it rather than writing it.

Reported as findings, never patched:

- bare **Pods** — `securityContext` is immutable on an existing pod;
- **Jobs** — `spec.template` is immutable after creation;
- **CronJobs** and standalone ReplicaSets — out of scope (D-08);
- explicitly privileged containers, and any explicit value weaker than the policy (BR-01);
- a request whose effective value is defaulted from a limit (BR-01);
- an absent limit, naming LimitRange as the remedy (BR-03);
- gaps covered by a LimitRange (BR-06).

Findings are enumerated whether or not they can be acted on. A namespace reported as hardened
while a root pod runs in it is a lie, and silence is how that lie gets told.

### BR-05 — Protected namespaces

Never target `kube-system`, `kube-public`, `kube-node-lease`, or the controller's own namespace.
Additional protected namespaces are configurable, using the same flag as 001. Naming a protected
namespace rejects the whole object: nothing is previewed and nothing is written.

### BR-06 — LimitRange, and why ResourceQuota is not checked

If a `Container`-scoped LimitRange in the namespace already supplies a resource this tool would
fill, that gap is reported as **covered by** that LimitRange and is not patched. The API server
injects those values into every pod it admits, so writing them into the template would restart
every pod in the namespace to change nothing.

Both `defaultRequest` **and `default`** supply a request. Where `defaultRequest` is omitted,
Kubernetes uses `default` for it, and even by the plainer route a defaulted limit is copied to
the request when the Pod is defaulted. A LimitRange declaring only `default: {memory: 512Mi}`
therefore gives every container an effective request of 512Mi, and reading `defaultRequest`
alone would report a gap that does not exist — BR-01's mistake, one scope up.

If a LimitRange declares `min`/`max` for a resource without a default, the requested values are
validated against them and the namespace is **rejected** when they fall outside. A template
whose pods the LimitRange rejects patches cleanly and then stalls the rollout, because
LimitRange is enforced at **pod** admission, so the API server's dry-run against the workload
object cannot catch it. This is a correctness rule, not an optimisation.

**ResourceQuota is deliberately not checked.** A quota constraining `requests.<resource>`
requires every incoming container to request that resource explicitly, so workloads with an
absent request cannot already be running in such a namespace — and where a LimitRange supplies
the default, the rule above has already removed the gap. The remaining case is close to
unconstructable, and any check would be an estimate against a `used` figure that moves. If it is
ever wrong the rollout stalls at pod creation, which is what the check would have predicted.

### BR-07 — Preview, then apply exactly what was approved, per target

`spec.approvedPlan` is a list of change hashes. Empty or absent, the object is **unarmed**: the
plan is computed, validated by a server-side dry-run of every changed target, and published in
status with a hash per target. Nothing is written.

Armed, each target is decided **on its own hash**:

| Target's current change hash   | Outcome                                    |
| ------------------------------ | ------------------------------------------ |
| present in `spec.approvedPlan` | patched                                    |
| changed since approval         | `Stale` — not patched, new hash published  |
| target appeared after approval | `Unapproved` — not patched, hash published |

**How `Stale` is told from `Unapproved`.** Both rows above are claims about the past, and
`approvedPlan` is a flat list of hashes with no target attached, so neither is decidable from
`spec` alone. They are resolved against the plan the controller last published in `status.plan`:
a target for which an earlier pass published a hash that appears in `spec.approvedPlan`, and
whose current hash differs from it, is `Stale`; every other target not approved now is
`Unapproved`. Where no such record exists — a first armed pass carrying hashes copied from
elsewhere, or a status that was cleared — the target reports `Unapproved`.

That degradation is deliberate, and it is bounded to the label. The **phase** never depends on
it: an approval that no longer describes anything is caught by FR-06's unmatched-hash rule,
which compares `approvedPlan` against the current plan and needs no memory at all. A lost
`status.plan` therefore costs an operator the word `Stale` on one row, never a request that goes
terminal having done nothing. `Stale` is the *attribution* — which target your approval used to
describe — and attribution is the only part that requires remembering.

The two rules cannot disagree. A change hash is taken over a canonical form prefixed with the
target's own namespace, kind and name, so distinct targets hash distinct inputs: a `Stale`
target's previously approved hash no longer describes that target and can describe no other, so
it is necessarily among the unmatched hashes that FR-06 already holds the object open for.

Per target, not per plan, because a single plan-wide hash cannot converge in a live environment:
any CI deploy touching any workload in any of up to 16 namespaces moves it, so the operator
re-copies the hash and is stale again before the write lands. It also contradicts this
document's own execution model, which reserves all-or-nothing for validation and makes execution
per-target (FR-05). Hashing per target needs no snapshot of the approved plan — the approval
_is_ the list — and lets an operator approve a subset deliberately, which is the normal way to
use a gate like this.

The guarantee is exact, and narrower than it may read: a target is patched only if the **set of
fields to be written, and their values, is unchanged** since the operator saw it. A target
edited in a way that does not change its gaps — a new image, a different command — has the same
hash and is still patched. That is the intended scope; overstating it would be worse than
stating it.

`approvedPlan` is the only mutable field in `spec`; everything else is immutable by a CEL
transition rule, as in 001, so retargeting means a new object.

### BR-08 — Provenance on the object that was changed

Every patch carries, **in the same request**, an annotation `hardening.acme.corp/filled` on the
target, recording the **leaf** path of every field written and the value written to it.

It lives on the target rather than only in status for two reasons. Status dies with the custom
resource — one-shot semantics and no finalizer mean deleting the object destroys the record. And
an operator inspecting a workload should be able to see what changed it without knowing this
tool exists.

Leaf paths, not the shallowest path created. Recording `resources` rather than
`resources.requests.cpu` would make a later removal take out a `limits` block a human added,
and that safety property is worth more than avoiding a cosmetic empty `{}` where a field is
removed.

It is the checkpoint a manual revert works from, though not a complete one: a target patched by
a second object has its annotation replaced, and the first object's record is lost. Nothing here stops two objects naming the same namespace — 001 refuses that case, this
feature does not (G-03).

### BR-09 — The rollout cost is the operator's to accept

A template patch restarts every pod of every target. The preview reports, per target, how many
pods are affected and which rollout mechanism applies (BR-02's table), so that a single-replica
StatefulSet is visibly a different proposition from a three-replica Deployment. The tool does
not stage, throttle or canary the rollout: the restarts are inherent to what was asked for, and
the workload's own `maxUnavailable` and readiness gating are the mechanism that bounds them.

## Functional requirements

### FR-01 — Interface

A namespaced CRD: group `hardening.acme.corp`, version `v1alpha1`, kind `WorkloadHardening`,
with a structural schema and the status subresource. Namespaces are named in `spec`, so the
object can live in a namespace of the operator's choosing.

```yaml
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata:
  name: tenant-hardening
  namespace: isolation-system
spec:
  namespaces: [tenant-a, tenant-b]
  resources:
    requests: { cpu: 10m, memory: 32Mi } # required; limits are never written (BR-03)
  securityContext:
    readOnlyRootFilesystem: false # optional; default false
  approvedPlan: [] # empty => preview only; the only mutable field
```

`namespaces` holds 1–16 unique DNS labels. `resources.requests` is required and must name both
`cpu` and `memory`, validated as quantities by the schema. There is no `resources.limits` field.
`approvedPlan` holds up to 128 twelve-character hex hashes; a plan larger than that is approved
in batches against the same object, which per-target semantics make safe and which FR-05's
generation gate is what permits — editing `approvedPlan` moves `metadata.generation`, so an
`Applied` object picks the next batch up. Namespace existence and LimitRange bounds are
checked by the controller and reported in status.

Creating the object requests a **preview**. Copying the hashes of the changes the operator
accepts out of `status.plan[].hash` into `spec.approvedPlan` requests those patches:

```console
$ kubectl -n isolation-system get workloadhardening tenant-hardening \
    -o jsonpath='{.status.plan[*].hash}'
```

There is no enable flag, and no undo in this version.

### FR-02 — The plan

A pure function, `plan.Build(template, policy) → Plan`, where `Plan` carries the `Change` list
and the `Finding` list, with no client and no cluster state beyond the arguments it is given —
the counterpart of `policy.Build` in 001. The same function feeds the dry-run, the real patch
and the rendered status, so a preview cannot diverge from what is applied.

Findings come back alongside changes because BR-04 requires them enumerated and both fall out of
the same traversal: deciding that a container's effective request is defaulted from its limit is
itself what produces the finding, so collecting them in a second pass would re-derive every
effective value the first had already computed.

Gaps are decided on **effective values** (BR-01), which means the function models Kubernetes'
own defaulting: pod → container precedence for securityContext, limits → requests for resources,
and the namespace LimitRange for both. None is observable in the template, so none can be
skipped.

Pod-level `runAsNonRoot` is emitted only when no container in the pod evidences a need for root
(BR-02). A container overriding a pod-level field to a weaker value is reported, because the
container wins.

Each target yields at most one **strategic merge patch**, with the container list keyed by
`name`. The patch body carries only the containers that have gaps and only the fields being
filled: it never restates the container array, never reorders it, and never mentions a container
with no gaps. A target with no gaps yields no patch and no API call.

### FR-03 — Preview

Every **changed** target's patch is issued with `DryRun: [All]`, so the preview reflects what the
API server accepts rather than what the tool believes it would accept.

While the object is **unarmed**, a target whose change hash is unchanged since the last pass is
not re-sent. Otherwise a `Previewed` object re-runs the full admission chain, every mutating and
validating webhook included, for every target in up to 16 namespaces on every resync, forever,
on behalf of an object nobody armed.

An **apply always dry-runs first**, cache or not. The cache key is the target's own change,
which does not move when a webhook is installed, a namespace gains a Pod Security label or a
LimitRange appears — so a cached acceptance says nothing about whether the write will be
accepted now. NFR-02 means a dry-run of this patch, against this cluster, in this pass.

The dry-run is side-effect-free only for webhooks declaring `sideEffects: None` or
`NoneOnDryRun`; the API server refuses a dry-run that would reach one declaring otherwise, and
that refusal is reported as the target's outcome rather than silently swallowed.

A refusal during a **preview** does not requeue: the object stays `Previewed`, the target's row
carries the refusal, and the periodic resync retries it. Returning an error would spin the
queue's backoff against a webhook that may refuse permanently, on behalf of an object nobody
armed. During an **apply** the same refusal is a `Failed` target and does return an error, so
the queue retries it, per FR-04.

What the dry-run does **not** cover is stated in Context and BR-02: root-related failures are
kubelet- and kernel-enforced, and pass a dry-run cleanly.

Status renders the plan per target: the object reference, its change hash, the fields that would
be set with their values, the number of pods affected, the rollout mechanism that applies, and,
for anything not patched, the reason.

### FR-04 — Apply

Targets whose change hash appears in `spec.approvedPlan` are patched, in a deterministic order
(namespace, kind, name), so a retry resumes predictably and the log reads in the same order as
the preview. Each patch carries its provenance annotation in the same request, so a target is
never patched without its record. Targets that are `Stale` or `Unapproved` are reported with
their current hash and not patched (BR-07).

On failure of an approved patch: keep what succeeded, record per-target outcomes, report
`PartiallyApplied`, and return an error so the queue retries. The retry recomputes — and because
of BR-01 an already-patched target has no gaps left, so it addresses only what failed. Never
roll back: a half-hardened namespace is not improved by un-hardening the half that worked.

An `Unapproved` target is not a failure and never triggers a retry (FR-06).

### FR-05 — Reconciliation

One controller process, sharing 001's binary, queue and worker. Watch `WorkloadHardening` only:
no workload informer, no drift repair. The periodic resync recomputes the plan so a published
preview does not silently rot, re-issuing dry-runs only for targets whose change has moved
(FR-03).

Per pass: read the object; if the phase is `Applied` and `status.observedGeneration` equals
`metadata.generation`, do nothing; validate namespaces and LimitRange bounds; enumerate targets;
build the plan; then preview or apply per BR-07. API calls carry timeouts.

`Applied` is the only terminal phase, and it is terminal **until the approval changes**.
`approvedPlan` is the only mutable field in `spec` (BR-07), so a `metadata.generation` ahead of
`status.observedGeneration` is exactly an operator extending or correcting an approval — which
is how BR-07's deliberately approved subset is extended, and how FR-01's batches reach a single
object. Without that gate the only editable field on the object would be ignored the moment the
first batch landed, and an approval that matched nothing could never be corrected. A resync does
not move `generation`, so an untouched `Applied` object still issues no API calls at all (AC-14).

`Rejected` is **not** terminal — it is re-evaluated on every resync, so an object refused for a
missing namespace or out-of-range LimitRange bounds recovers by itself once the cause clears, as
in 001. A protected namespace is a permanent
condition and will be re-evaluated pointlessly forever; that is accepted, because a second
terminality rule costs more than the wasted comparison.

Validation is **all or nothing across namespaces**: if one named namespace is missing or
refused, the object is rejected and no namespace is patched. The operator named two namespaces
and should get both or neither. Execution, in contrast, is per-target (FR-04, BR-07).

No finalizer. Deleting the object leaves the patches and the provenance annotations in place, so
no object is ever stuck waiting on this controller.

### FR-06 — Status

| Phase              | Meaning                                                                                                               |
| ------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `Pending`          | Not yet evaluated                                                                                                     |
| `Rejected`         | A precondition failed; nothing was written                                                                            |
| `Previewed`        | A plan was computed and accepted by dry-run; nothing was written                                                      |
| `Applied`          | Every hash in `spec.approvedPlan` matched a target, every approved target was patched, none failed and none is `Stale` |
| `PartiallyApplied` | At least one approved target failed or is `Stale`, or at least one approved hash matched no target                    |

Per-target outcome is one of `Planned`, `Patched`, `Failed`, `Stale`, `Unapproved`, or a finding
reason. `Planned` is the outcome of every row on an unarmed object: the target has gaps, its
patch was accepted by the dry-run, and it is waiting to be approved. None of the other values
describes that state — nothing was written, and `Stale` and `Unapproved` are both statements
about an approval that an unarmed object does not have — so without it the most common row in
the feature would carry an empty outcome, which reads as a fault rather than as a target
awaiting the operator.

`Unapproved` is **not** a failure. Approving a subset is the expected use of a per-target gate,
so an object whose approved targets all patched is `Applied` however many targets it left alone;
the count and their hashes appear in the message and the per-target plan. `Stale` is a failure
of a different kind — the operator approved a change that no longer exists — so it holds the
object in `PartiallyApplied`, which is non-terminal and re-evaluated until the approval is
updated.

An approved hash that matches **no target at all** is neither of those, and both rows of the
table would otherwise be silent about it: the phases are defined over approved *targets*, and
`approvedPlan` is a list of *hashes*. Where every hash dangles, "every approved target was
patched" is vacuously true over an empty set; where only some dangle, the matched ones patch and
carry the object to `Applied` on their own. Either way `Applied` is terminal, so the approval
would be stranded having done nothing. It therefore holds the object in `PartiallyApplied`, with
the unmatched hashes named in the message. It arises from a copy-paste of a stale preview, a
mistyped hash, or a target whose gaps someone else closed between preview and approval.

Unlike `Stale`, recognising this needs no memory of what was previously published. `Stale` is a
property of a target — its change moved since approval — so identifying one requires the plan
published earlier, and degrades to `Unapproved` when that record is gone. An unmatched hash
matches nothing *now*, whatever it matched before, so the phase never depends on status history
even though a per-target label may.

A row whose outcome is `Patched` is **retained across later passes**. An already-patched target
has no gaps left (BR-01), so it drops out of the recomputed plan entirely, and an `Applied`
object whose plan rendered empty would tell an operator nothing about what the tool did to their
namespaces.

Status carries the phase, a message naming the specific cause, the per-target plan or outcome
with its current change hash, the pods affected, the findings with their reasons, the last
reconcile time, and `observedGeneration` — the `metadata.generation` of the spec this status
describes, which is what FR-05 compares to decide whether an `Applied` object has a new
approval to act on. It is written only when something other than the timestamp changed, as in 001,
so a resync of an unchanged object issues no writes at all.

`Applied` means the API server accepted every approved patch. It does **not** assert that the
resulting pods became Ready — that is the rollout's business and the operator's to watch (G-02).

## Non-functional requirements

- **NFR-01 — Simplicity.** Idiomatic Go with client-go, no new dependencies. The plan is a pure
  function in its own package, as `pkg/policy` is in 001; discovery and patching are the only
  parts that touch the cluster.
- **NFR-02 — Safety.** No write outside a named namespace. No write to a field whose effective
  value is already present. No write to a target whose change hash was not approved. No write
  before a dry-run of that same patch, in the same pass, has been accepted. No patch without its
  provenance annotation in the same request.
- **NFR-03 — Access.** The ClusterRole adds `deployments`, `statefulsets` and `daemonsets`
  get/list/patch cluster-wide — namespaces are chosen at runtime, so this cannot be
  namespace-scoped; `pods`, `jobs`, `cronjobs`, `replicasets`, `limitranges` and `namespaces`
  get/list — `cronjobs` because BR-04 requires findings enumerated whether or not they can be
  acted on, and a CronJob between schedules owns no Job to be reported through;
  `workloadhardenings` plus its status. No `resourcequotas` (BR-06), no `delete` on any
  workload, no pod exec, no secrets. 001's lesson applies: `patch` is a distinct verb from
  `update`, and only an in-cluster run catches a missing one.
- **NFR-04 — Visibility.** Log the object UID, the decision taken, the per-target outcome and
  hash, and every refusal with its reason.
- **NFR-05 — Verification.** Every acceptance criterion has an automated test; each code unit
  reaches 90% unit coverage per `AGENTS.md`. Fake clients do not run Kubernetes' defaulting and
  are not evidence that a patched workload still runs, so AC-03's limits→requests rule, AC-06's
  LimitRange rule and AC-05's kubelet behaviour are pinned by the kind script.
- **NFR-06 — Reproducibility.** CRD, RBAC and controller manifests, sample workloads covering
  each finding class, Makefile targets, and README decisions.

## Acceptance criteria

| ID    | Scenario                                                                                                                                                                                                                                                                                                                                                                                                              | Evidence |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| AC-01 | A template missing everything yields one patch per target carrying exactly the four always-on fields at the correct levels, `initContainers` included, `ephemeralContainers` untouched, and no other field                                                                                                                                                                                                            | Unit     |
| AC-02 | securityContext precedence: pod-level `runAsNonRoot: true` with a container-level `false` is a finding, not treated as hardened; a container-level `true` with nothing at pod level is left alone                                                                                                                                                                                                                     | Unit     |
| AC-03 | **Effective requests:** a container with `limits` set and `requests` absent has **no** gap and yields the "defaulted from limit" finding; one with neither yields a gap; one with explicit requests is untouched                                                                                                                                                                                                      | Unit     |
| AC-04 | `readOnlyRootFilesystem` appears in no patch unless requested, and in every eligible container when it is. No patch ever contains a `limits` key                                                                                                                                                                                                                                                                      | Unit     |
| AC-05 | **Root evidence:** a privileged container, and an effective `runAsUser: 0`, each suppress pod-level `runAsNonRoot` for the whole pod and yield a finding; the other three fields are still written **for the pod and its other containers**, while the privileged container itself receives none — the API server rejects `allowPrivilegeEscalation: false` beside `privileged: true`, which would fail the whole target's dry-run and take its siblings with it                                                                                                                                                                                                                    | Unit     |
| AC-06 | **LimitRange:** a `defaultRequest` covering memory, **and a `default` with no `defaultRequest`**, each report the memory gap as covered and omit it from the patch; a `min`/`max` excluding the requested values rejects the namespace and writes nothing                                                                                                                                                             | Unit     |
| AC-07 | A `paused` target, an `OnDelete` target and a StatefulSet with `partition > 0` are each refused with a distinct reason and never patched                                                                                                                                                                                                                                                                              | Unit     |
| AC-08 | Preview writes nothing: every changed target's patch is issued with `DryRun`, stored objects are unchanged, and the per-target plan and hashes appear in status                                                                                                                                                                                                                                                       | Unit     |
| AC-09 | Per-target approval: an approved target is patched while an unrelated workload appearing in the same namespace is reported `Unapproved` without blocking it or the phase; a target whose own change moved is `Stale` and holds the object in `PartiallyApplied`                                                                                                                                                       | Unit     |
| AC-10 | While unarmed, an unchanged target is not re-dry-run on resync and a changed one is; an apply dry-runs every approved target regardless of the cache                                                                                                                                                                                                                                                                  | Unit     |
| AC-11 | Provenance: every applied patch carries the annotation in the same request, recording **leaf** paths and the values written                                                                                                                                                                                                                                                                                           | Unit     |
| AC-12 | Excluded and unpatchable: a protected namespace rejects the object; a skip-annotated target, a ReplicaSet owned by a Deployment, a bare Pod and a Job are each reported with a distinct reason                                                                                                                                                                                                                        | Unit     |
| AC-13 | Partial failure keeps what succeeded, reports `PartiallyApplied`, and converges on retry without re-patching what already landed                                                                                                                                                                                                                                                                                      | Unit     |
| AC-14 | An `Applied` object issues no API calls on resync, including one that reached `Applied` with targets left `Unapproved`; a `Rejected` one is re-evaluated and reaches `Previewed` once the cause clears                                                                                                                                                                                                                | Unit     |
| AC-15 | On kind: a Deployment with no requests and a root container is previewed, approved by hash, applied, rolls out and stays Ready; a Deployment declaring **both** `cpu` and `memory` limits and no requests is **not** patched and keeps QoS `Guaranteed`; a namespace whose LimitRange sets only `default` reports its gaps as covered; a skip-annotated Deployment is untouched; the provenance annotation is correct | Script   |
| AC-16 | The CRD installs and the API server rejects an empty namespace list, a missing `resources.requests`, a `resources.limits` key, and an edit to any field other than `approvedPlan`                                                                                                                                                                                                                                     | Script   |
| AC-17 | **Unmatched approval:** a hash in `spec.approvedPlan` matching no target holds the object in `PartiallyApplied` and is named in the message — including when another approved target patched successfully in the same pass, which `Applied` would otherwise claim                                                                                                          | Unit     |
| AC-18 | **Approval extended after `Applied`:** adding a hash to `spec.approvedPlan` on an `Applied` object is re-evaluated, patches the newly approved target, and does not re-patch one already patched; an `Applied` object whose generation has not moved still issues no API calls                                                                                              | Unit     |

**AC-03 and AC-05 are the two most easily got wrong.**

AC-03 is the sharper one. Nothing in a workload's YAML shows that a `limits`-only container has
an effective request equal to its limit — the defaulting happens when the **Pod** is created, so
the template a tool reads is silent about it. Measured on a live cluster: a Deployment declaring
`limits: {cpu: 500m, memory: 1Gi}` and no requests runs as QoS `Guaranteed` with
`requests == limits`; writing `requests: {cpu: 10m, memory: 32Mi}` into it cuts the CPU
reservation 50×, the memory reservation 32×, and demotes it to `Burstable`. A hardening tool
would degrade the workload it was asked to protect, pass its own dry-run, and report success.
AC-06 is the same trap one scope up, sourced from the namespace instead of the container, and
AC-15 keeps both honest against a real cluster because a fake client does not run Kubernetes'
defaulting.

AC-05 is this feature's counterpart to 001's `NotIn`-matches-absent-key trap: the field you read
is not the value that applies. Its specific trap is that `runAsNonRoot` is written at _pod_
level, so a single privileged container — reported as a finding, which reads as "left alone" —
still gets `runAsNonRoot: true` imposed on it and fails with `CreateContainerConfigError`. On a
DaemonSet that takes out a node's agent, and no dry-run refuses it.

## Error cases

| Case                                                                         | Behaviour                                                                                                                                                                                                                                                                                                                                        |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Invalid shape, or an edit to any field but `approvedPlan`                    | Rejected by the API server; the stored object is untouched                                                                                                                                                                                                                                                                                       |
| Protected namespace named in the request                                     | `Rejected`; nothing previewed, nothing written                                                                                                                                                                                                                                                                                                   |
| A named namespace is missing                                                 | `Rejected`, naming it; no other namespace is patched either (FR-05)                                                                                                                                                                                                                                                                              |
| LimitRange bounds exclude the requested values                               | `Rejected`, with the bounds in the message                                                                                                                                                                                                                                                                                                       |
| The dry-run rejects a patch, or would reach a webhook declaring side effects | That target's outcome records the API server's message; other targets are unaffected                                                                                                                                                                                                                                                             |
| A target's change moved since approval                                       | `Stale`; not patched; its new hash is published for re-approval; the object is `PartiallyApplied`                                                                                                                                                                                                                                                |
| A target appeared after approval                                             | `Unapproved`; not patched; its hash is published; the phase is unaffected                                                                                                                                                                                                                                                                        |
| A hash in `approvedPlan` matches no target                                   | `PartiallyApplied`, naming the unmatched hashes; non-terminal, so correcting the approval is acted on. Applies equally when other approved targets patched in the same pass (FR-06)                                                                                                                                                             |
| `approvedPlan` is extended or corrected after `Applied`                      | `metadata.generation` moves, so the object is re-evaluated (FR-05). Newly approved targets are patched; targets already patched have no gaps left (BR-01), so they are not patched again                                                                                                                                                        |
| A target vanishes between preview and apply                                  | Absent from the recomputed plan, so it is simply not patched                                                                                                                                                                                                                                                                                     |
| A target is edited without changing its gaps                                 | Same hash, so it **is** patched. Stated in BR-07 rather than claimed otherwise                                                                                                                                                                                                                                                                   |
| One approved patch fails during apply                                        | `PartiallyApplied`; successful patches kept; retried; recomputation means only the failures are retried                                                                                                                                                                                                                                          |
| Patched pods never become Ready                                              | Not detected. `Applied` reports acceptance, not a successful rollout. The rollout halts per BR-02's table; the remedy is to revert the template by hand, which the provenance annotation records exactly                                                                                                                                                                                                           |
| A patched pod becomes Ready and fails later                                  | Possible for the three kernel-enforced fields — a denied syscall, a missing capability, a setuid exec — and not detected. Accepted: those three are the restricted PSS baseline and are the feature. `readOnlyRootFilesystem` is opt-in and limits are never written because for them the late failure is the expected case rather than the tail |
| Quota exceeded at pod creation                                               | The rollout stalls. Not pre-checked, by decision (BR-06)                                                                                                                                                                                                                                                                                         |
| Controller unavailable                                                       | Nothing happens. With no finalizer, no object is ever stuck waiting on this controller                                                                                                                                                                                                                                                           |
| API error during apply                                                       | Recorded as that target's outcome and retried; never assumed to have landed                                                                                                                                                                                                                                                                      |

## Out of scope

What this feature does not do, split by whether the exclusion is a choice (`D-`, declined) or a
gap (`G-`, not yet built).

### Declined

Considered and deliberately not built. More time would not change these.

| #    | Excluded                                                                    | Why                                                                                                                                                                                                                                                                                                       |
| ---- | --------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D-01 | An admission webhook rejecting or mutating unhardened workloads at creation | The brief asks to patch what is already running. Prevention is a different feature, and Pod Security admission plus a LimitRange are the cluster's native mechanisms for new pods. Consequence: a workload created after an apply is unhardened and needs a new object.                                   |
| D-02 | Drift repair, and re-patching a workload someone later unhardened           | The tool does not own the fields it writes. A reconcile loop would overwrite a deliberate later change and restart pods to do it.                                                                                                                                                                         |
| D-04 | Overwriting any value already present, including a weaker one               | BR-01. The tool reports what a human chose; it does not overrule it.                                                                                                                                                                                                                                      |
| D-05 | Writing `runAsUser`, `fsGroup` or `privileged`                              | A guessed UID breaks file ownership silently; privilege that was asked for is needed. BR-02.                                                                                                                                                                                                              |
| D-06 | `readOnlyRootFilesystem` by default                                         | The policy field whose failure is reliably late: a container that writes to its filesystem generally does so after it is serving, so nothing halts and every pod has already been replaced. Not part of restricted PSS either.                                                                            |
| D-07 | Writing resource **limits** at all                                          | The tool cannot know a workload's working set, and neither can one number spread across 16 namespaces. The failure is load-dependent, so it survives a green rollout by design rather than by accident. LimitRange is the per-namespace mechanism that exists for this, and BR-06 routes operators to it. |
| D-08 | Patching CronJobs, Jobs, bare Pods and standalone ReplicaSets               | A CronJob needs a second template path and has no rollout net at all — a broken one simply fails on its next schedule. A Job's template and a Pod's `securityContext` are immutable where it matters. A standalone ReplicaSet is rare. All four are reported.                                             |
| D-09 | Recreating a Pod or Job in order to harden it                               | Deleting someone's workload to improve its securityContext is not a trade this tool makes.                                                                                                                                                                                                                |
| D-10 | Staging, throttling or canarying the rollout                                | BR-09. `maxUnavailable` and readiness gating already bound it, and reimplementing that is a deployment controller.                                                                                                                                                                                        |
| D-11 | A namespace selector, a workload selector, or cluster-wide targeting        | An explicit list is what the brief describes, and it keeps the blast radius readable in the object itself.                                                                                                                                                                                                |
| D-12 | Right-sizing requests from observed usage                                   | That is a VPA. It needs metrics history this tool does not have, and it would mean overwriting values, which BR-01 forbids.                                                                                                                                                                               |
| D-13 | Server-side apply with a dedicated field manager                            | It would give a cleaner ownership story, at the cost of permanently changing field ownership on objects other people `kubectl apply`, handing them conflicts later. Worth reconsidering if field ownership becomes contested.                                                                           |
| D-14 | Deciding what to patch by reading running pods                              | Templates plus Kubernetes' documented defaulting rules are the source of truth (BR-01). Pods appear only as findings.                                                                                                                                                                                     |
| D-15 | A pre-flight ResourceQuota check                                            | BR-06. The reachable case is close to unconstructable and any check is an estimate against a moving `used`.                                                                                                                                                                                               |
| D-16 | Helm packaging, multiple served CRD versions, and request history           | Not required by the brief.                                                                                                                                                                                                                                                                                |

### Deferred

Real gaps, not present in this version. Ordered by what would be addressed first.

| #    | Not yet done                                                          | Interim position                                                                                                                                                                                                                                                                                                                            |
| ---- | --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| G-02 | Watching the rollout to completion                                    | `Applied` means the API server accepted every approved patch. A halted rollout is visible in the workload but not in this object's status, so the tool's own report is not trustworthy end to end — and BR-02's per-kind table is exactly what an operator would want surfaced here.                                                        |
| G-03 | Per-namespace exclusivity between objects                             | Two objects may target the same namespace, and the second one's provenance annotation replaces the first's (BR-08). 001 refuses this case per namespace; this feature does not.                                                                                                                                                             |
| G-04 | Gating the configurations BR-02's table shows are worst               | A single-replica StatefulSet loses its only pod, and a DaemonSet whose container runs as root via the image's `USER` alone cannot be detected from the template and costs a node. Making pod-level `runAsNonRoot` opt-in for DaemonSets would close the second. Today BR-09 reports the mechanism and pod count and nothing refuses either. |
| G-05 | Conditions and per-target conditions in status                        | Phase, message and the per-target plan are enough to operate the tool, but not enough to automate against it. Shared with 001's G-05. `observedGeneration` is **no longer deferred**: FR-05 needs it to tell a new approval from a resync, so it is part of this version.                                                                    |
| G-06 | The kind verification in CI, plus envtest for the CEL rule and schema | Runnable scripts cover AC-15 and AC-16; fake clients cannot exercise either, nor Kubernetes' own defaulting.                                                                                                                                                                                                                                |
| G-07 | A metrics endpoint                                                    | Listed as a bonus. Shared with 001's G-07.                                                                                                                                                                                                                                                                                                  |
| G-08 | Leader election                                                       | Shared with 001's G-01, but the exposure is smaller here: two writers would compute the same plan and issue the same gap-filling patches, so the second is a no-op.                                                                                                                                                                         |

## References

- [Resource management: requests, limits and QoS](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/) — limits→requests defaulting, QoS classes
- [LimitRange](https://kubernetes.io/docs/concepts/policy/limit-range/) — `default` supplies `defaultRequest` when the latter is omitted
- [Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
- [Pod Security Admission](https://kubernetes.io/docs/concepts/security/pod-security-admission/) — `enforce` applies to pods; workload resources get `warn` and `audit`
- [Configure a Security Context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/)
- [Seccomp](https://kubernetes.io/docs/tutorials/security/seccomp/) — `RuntimeDefault` denials surface when the syscall is made
- [Deployment rolling updates](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#rolling-update-deployment) — `maxSurge`/`maxUnavailable` defaults and rounding
- [StatefulSet rolling updates](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#rolling-updates) — reverse ordinal, terminate-then-create, no surge, `partition`
- [DaemonSet rolling updates](https://kubernetes.io/docs/tasks/manage-daemon/update-daemon-set/) — `maxUnavailable: 1`, `maxSurge: 0`
- [Server-side dry-run](https://kubernetes.io/docs/reference/using-api/api-concepts/#dry-run) — webhook side effects
- [Update API objects in place using kubectl patch](https://kubernetes.io/docs/tasks/manage-kubernetes-objects/update-api-object-kubectl-patch/) — strategic merge patch and merge keys
