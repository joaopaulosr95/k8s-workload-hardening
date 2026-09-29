---
name: Bonus
description: |
  Undo for workload hardening, a metrics endpoint, integration tests on kind, and a naming
  and documentation refactor across 001 and 002. Undo is specified in full; the other three
  are scoping notes.
author: João Bastos <joaopaulosr95@gmail.com>
status: Draft
relatedResources:
  - docs/assignment.md
  - specs/001-network-isolation/spec.md
  - specs/002-workload-hardening/spec.md
---

# Bonus

## Objective

Four items outside the two core tasks: a refactor, an undo for workload hardening, a metrics
endpoint, and integration/e2e tests on kind.

## Context

Only one of the four is a feature. **Undo** writes to workloads other teams own, has a failure
mode that halts rollouts, and depends on an admission behaviour no dry-run reports — so it
carries business rules, acceptance criteria and error cases of its own, below. The other three
change how existing work is named, packaged or run; they are scoped in a paragraph each and
deliberately not given the apparatus of a spec they do not need.

002's D-03 and G-01 point here for the undo, and BR-08 exists to make it possible. This
document is what those references resolve to.

## Refactor

Isolation was built under the assumption nothing else would exist in the project, so its types
took the unqualified names and hardening got the prefixed ones. The result is four collisions:

| Collision                                                              | Where                                      |
| ---------------------------------------------------------------------- | ------------------------------------------ |
| `v1alpha1.Spec` / `Status` (isolation, unqualified) vs `HardeningSpec` / `HardeningStatus` | `types.go:58,71` vs `hardening.go:78,127`  |
| `plan.Finding` vs `v1alpha1.Finding`                                   | `plan.go:68`, `hardening.go:117`           |
| `Reconciler` vs `HardeningReconciler`                                  | `reconcile.go:29`, `targets.go:33`         |
| **`plan.Policy` vs package `pkg/policy`** — unrelated things sharing a word | `plan.go:83`, `pkg/policy`            |

Fix direction: make the **001** side explicit rather than the 002 side implicit.
`Spec`→`IsolationSpec`, `Status`→`IsolationStatus`, `Reconciler`→`IsolationReconciler`,
`types.go`→`isolation.go`, `reconcile.go`→`isolation.go`. `setHardeningStatus`→`setStatus`
comes free once the receivers are distinct. Rename `plan.Policy`→`plan.Request` and leave the
package names alone: the `pkg/policy` / `pkg/plan` asymmetry is cosmetic next to a type and a
package that mean different things by the same word.

No behaviour changes, so the existing tests are the proof. Anything that needs a test edit
beyond a rename is a finding, not a refactor.

**Build and manifests.** The same asymmetry outside Go. `make verify` runs isolation while
`make verify-hardening` runs hardening; likewise `samples` / `samples-hardening` and
`verify-crd` / `verify-crd-hardening`. Rename the 001 side to `verify-isolation` and
`samples-isolation`, and keep a `verify` that runs both — which is what the CI job in the
integration-tests section needs anyway.

`deploy` restarts `deployment/network-isolation` in namespace `isolation-system`: one controller
process serving both CRDs, named after the first one written. Renaming it is the only item in
this whole refactor that is **not** free — it moves an RBAC subject and every script that names
it — so it is a decision rather than a rename, and skipping it costs nothing but a misleading
name.

**Docs.** The README was written for 001 and 002 was appended: `# core task 2` sits at line 54
inside `## Setup`, and lines 269–403 duplicate the whole structure (Decisions, Limitations,
What I'd do, Time spent at 297/334/363/391 mirroring 98/166/200/263). Merge per topic, with
both features in each section and a single Time spent table. One pass, one decision.

**Architecture.** The line counts point at one function: `HardeningReconciler.discover`,
`targets.go:145–310`. Everything else in the non-test code is proportionate. The mass is in the
tests — `hardening_test.go` is 1407 lines against a 348-line source — which is worth one pass
for table consolidation and no more.

## Undo for workload hardening

Cheaper than it looks, because of BR-01 in `specs/002-workload-hardening/spec.md`: the tool only
ever fills a field whose **effective value** is absent, and never overwrites one. The inverse of
every change is therefore "delete this field" — there is no previous value to snapshot.

### Terminology

Inherits 002's. Two additions:

- **Record:** one `path=value` line of the `hardening.acme.corp/filled` annotation — a leaf path
  this tool wrote, and the value it wrote, in the rendering `plan.Lines` produces.
- **Deletion path:** the path actually sent as `null`. Usually the record's own leaf; not always
  (BR-U03).

### BR-U01 — The checkpoint is the annotation, not status

The provenance annotation on the patched workload (002's BR-08), never `status.applied[]`.
Status dies with the custom resource: one-shot semantics and no finalizer mean deleting the
`WorkloadHardening` destroys any record living there. The annotation outlives it, which is what
makes an undo a standalone operation — it needs a list of namespaces, not the original object.

Nothing to undo if the workload was replaced: delete and recreate it and the annotation goes
too, correctly, because the new template was never patched.

### BR-U02 — Delete a field only if its current value still equals the record

The annotation records values, not just paths, precisely so that a human's later edit is left
alone. BR-01's "never overrule a human" rule pointed the other way; this is the same rule read
backwards.

The comparison is on the **rendered** form, not the typed value: the annotation is a flat
`path=value` text blob (`plan.Provenance`), and `capabilities.drop` is recorded as the literal
string `[ALL]` (`plan.go:269`), not as JSON. An undo re-renders the live value through the same
`plan.Lines` path and compares strings. Comparing a `[]corev1.Capability` to a record is the
obvious first mistake and AC-U02 exists to catch it.

A record whose live value differs is a finding — "edited since hardening; left alone" — not a
failure.

### BR-U03 — The deletion path is the leaf, except where its parent cannot exist empty

Leaf paths are why an undo is safe at all: deleting `resources` wholesale would take a `limits`
block a human added after the apply. So the record is a leaf, and the deletion is normally that
same leaf.

One field breaks it. `seccompProfile.type` is a required union discriminator, so
`seccompProfile: {}` fails API validation — and 002's BR-02 writes `seccompProfile` **always**,
so it is on every hardened target. Since a target yields exactly one strategic merge patch
(`plan.Patch`), that one field would take the whole target's undo down with it, requests
included. The dry-run refuses it, so it fails loudly rather than corrupting anything; it also
fails every time.

| Record                                      | Deletion path                          | Why                                                     |
| ------------------------------------------- | -------------------------------------- | ------------------------------------------------------- |
| `...securityContext.seccompProfile.type`    | `...securityContext.seccompProfile`    | `type` is required; the empty parent is invalid          |
| everything else                             | the leaf itself                        | `securityContext: {}`, `capabilities: {}`, `requests: {}` are all legal |

Deleting the parent is safe here for the same reason the record exists: BR-01 only filled
`seccompProfile.type` because its effective value was absent, and an existing `seccompProfile`
always carries a `type` — so the parent was created by this tool and holds nothing else.

One entry, so a table beats a general rule about required fields. If a second field ever joins
it, the table grows by a row.

### BR-U04 — Namespace admission gates the undo, in two places, and neither shows in a dry-run

Removing a field is not always allowed, and the refusals come from admission plugins that run
against **pods**, not against the workload object being patched. The workload patch succeeds,
every new pod is rejected, and the rollout halts — the failure class this whole feature exists
to avoid.

**Pod Security.** In a namespace labelled `pod-security.kubernetes.io/enforce: restricted`, all
four always-on fields of BR-02 are required by the standard: deleting `runAsNonRoot`,
`seccompProfile`, `allowPrivilegeEscalation` or `capabilities.drop` makes the template
non-compliant. `enforce: baseline` requires none of them — an absent `seccompProfile` is
baseline-compliant — so baseline does not gate. `readOnlyRootFilesystem` is in neither standard
and is always removable, as are resource requests.

The check reads the namespace's `enforce` label and is deliberately conservative: any
`restricted` blocks all four, whatever `enforce-version` pins. Refusing a removal that would
have been legal costs the operator a manual edit; the other error halts a rollout.

**LimitRange.** A Container-scoped LimitRange declaring `min` for a resource with no `default`
or `defaultRequest` rejects a pod whose request is absent. Removing the request we filled
therefore breaks pod admission in exactly the namespaces 002's BR-06 already reads LimitRanges
for — the same object, the opposite question. The case arises when the LimitRange was added
after the apply, or when it declares bounds without defaults; where it supplies a default, BR-06
reported the gap as covered and never filled it, so there is no record to undo.

A gated field is **skipped, not fatal**: the rest of that target's undo proceeds. A
`restricted` namespace still gets its requests back, and the four securityContext fields are
reported with the label that blocked them. Refusing the whole target would make the common case
— hardening that broke a workload in a hardened namespace — unrecoverable by this tool.

Not covered, and stated rather than implied: cluster-wide Pod Security defaults set through
`AdmissionConfiguration` rather than namespace labels, and third-party admission (Kyverno,
Gatekeeper) whose auto-generated rules may or may not target the workload object. Both are
D-U03.

**A free second signal.** Pod Security's `warn` and `audit` modes *do* evaluate workload
resources, so a dry-run in a namespace labelled `warn: restricted` returns a warning header
naming the violation — exact, and version-correct without modelling anything. It is not a
substitute: a namespace carrying only `enforce` emits nothing, which is the common case. So the
`enforce` label is the gate and the warning is reported alongside it.

### BR-U05 — The annotation is rewritten in the same request

Every undo patch carries, in the same request, the new value of `hardening.acme.corp/filled`:
the records that were **not** deleted — those a human edited (BR-U02) or admission gated
(BR-U04) — or removal of the annotation entirely when nothing survives.

Mirrors BR-08's rule in the other direction. Without it the annotation asserts fields the
workload no longer has, a second undo re-attempts paths that are already gone, and the record an
operator inspects to see what this tool did to their workload is a lie.

### BR-U06 — Selection, exclusion and refusal, with two deltas

002's BR-04 and BR-05 apply unchanged: 1–16 explicit namespaces, protected namespaces reject the
whole object, the same three kinds, controlled objects skipped, and `spec.paused`, `OnDelete` or
`partition > 0` **refused**. An undo patch sits just as inert on a workload that will not roll
out, and the operator's remedy — unpause, or roll manually — is named in the refusal. (A halted
rollout is not a `paused` Deployment, so the case is rarer here than it reads.)

Two deltas:

- **`hardening.acme.corp/skip` is ignored.** A target carrying both `skip` and `filled` was
  hardened before someone asked to be left alone. Removing the hardening honours that intent
  rather than contradicting it.
- **A ReplicaSet may carry the annotation.** The Deployment controller copies a Deployment's own
  annotations onto the ReplicaSets it creates, so old ReplicaSets carry stale `filled` records.
  BR-04's controlled-object exclusion already keeps them out of the target set, which makes it
  load-bearing here rather than cosmetic. AC-U08 pins it on a live cluster, because a fake
  client does not run the Deployment controller.

### BR-U07 — Preview, then apply exactly what was approved, per target

Unchanged from 002's BR-07 and reusing its machinery: `spec.approvedPlan` is a list of per-target
change hashes over the same `plan.Canonical` serialisation, empty means unarmed, and `Stale` and
`Unapproved` resolve against the last published plan the same way. An undo is previewable on
machinery that already exists, which is most of why it is cheap.

The hash is taken over the **deletion** plan, so it moves when a human edits one of the recorded
fields — which is exactly when the operator should look again.

### BR-U08 — The rollout cost, and two consequences worth naming

Costs a second rollout — removing requests restarts the pods again, with the same per-kind blast
radius as BR-02's table, reported per target as BR-09 requires.

- Removing a request returns the container to `BestEffort`, first evicted under node pressure.
  That is the mirror of BR-03's benefit, and it is the state the workload was in before.
- Where a human added `limits` after the apply, deleting the request **raises** the effective
  request to the limit, because Kubernetes copies limits into absent requests when defaulting
  the Pod. Correct, and not obvious: the undo of a 10m request can be a 500m reservation.

### BR-U09 — After an undo, nothing re-hardens by itself, and nothing is permanent either

An undone workload has absent effective values again, so BR-01 sees a genuine gap and 002 would
fill it. What stops that today is not a rule about the workload — it is FR-05's generation gate:
an `Applied` object whose `observedGeneration` equals its `metadata.generation` issues no API
calls at all (AC-14). The protection is a property of the request's lifecycle, and it has one
realistic hole.

FR-01 approves a large plan in batches against the same object, and FR-05's gate exists so an
`Applied` object picks the next batch up. The moment anyone edits `approvedPlan`, the plan is
recomputed and the undone target reappears in it with a **new hash**. It is `Unapproved`, so
nothing is written without approval — but it is one blanket copy-paste of the published hashes
away from being re-hardened. A deleted-and-recreated hardening object, or a second object naming
the namespace (G-03), reaches the same place from `Pending`.

**So an undo is a point-in-time correction, not an exemption, and the spec says so rather than
implying otherwise.** The exemption mechanism already exists and is 002's:
`hardening.acme.corp/skip: "true"` (BR-04), the escape hatch for a workload that genuinely needs
what the policy would take away — which is exactly what a workload someone undid has just
demonstrated.

`spec.exempt: true` on the undo object writes that annotation onto every target it reverts, **in
the same patch**. It costs nothing: BR-U05 is already rewriting `metadata.annotations` in that
request, so it is one more key and no extra API call and no window between the two writes.

It defaults to **false**, because reverting a change and refusing all future ones are different
decisions and an operator undoing a single bad rollout should not silently opt those namespaces
out forever. Setting it is also not a one-way door — `kubectl annotate --overwrite` removes it —
whereas a reverting undo that always exempted would make re-hardening an annotation-editing
exercise across every target.

### BR-U10 — Hardening stands down while an undo is live over the same namespace

**The failure is a loop, not just churn.** A `WorkloadHardening` H and a `WorkloadUndo` U both
naming `harden-a`: U reverts `api`, so its gaps reopen; H's next pass sees a genuine gap (BR-01
judges effective values, and they are absent again) and publishes a hash for it; H is armed, so
it re-patches; U's next pass sees the provenance annotation back and plans the revert again.
Every round trip rolls every pod of every affected target **twice**. With only one side armed it
degrades to the milder version — the unarmed side's published plan flips between "no row" and "a
row" on alternate passes, so the operator's copied hash dangles or goes `Stale` for reasons
nothing in status explains.

**Object-level refusal, not target-level skipping.** The tempting shape is the one you describe:
have hardening's `discover` drop the targets an undo is currently reverting. It is the worse of
the two:

- It makes H's plan depend on U's **status**, and 002 already treats published status as
  something that can be lost — BR-07's whole `Stale`-degrades-to-`Unapproved` argument exists
  because `status.plan` is not a reliable memory.
- It depends on U's **timing**. A freshly created U has published no plan, so H sees nothing to
  skip and proceeds; the race it was meant to close is still open on the pass that matters.
- It makes H's published hashes non-deterministic, since which targets are in the plan now
  depends on when another object last reconciled.

Refusal needs none of that: it is decided from `spec.namespaces` on both objects — a set
intersection, no status, no plan, no timing — and it is the shape 002's validation already uses
for preconditions, which FR-05 makes explicitly all-or-nothing across namespaces.

**Asymmetric: only hardening checks, and the undo ignores hardening entirely.** Making it
symmetric deadlocks. `Rejected` is not `Applied`, so two objects created together each see a
non-`Applied` counterpart, each rejects, and neither can ever reach a phase that releases the
other. Precedence to the undo breaks the cycle by construction, and it is the right way round:
an undo is a corrective action taken because something is broken, hardening is elective, and
when both are present the operator's live intent is to stop the damage.

So: **a `WorkloadHardening` is `Rejected` while any `WorkloadUndo` that is not `Applied` names
one of its namespaces.** `Applied` is the point after which an object writes nothing without a
new generation, so it is the correct line, and `Rejected` is non-terminal (FR-05), so H recovers
by itself on the resync after U finishes — no lock to release, no stale object to delete.

**Runtime cost is nothing.** Both kinds are already served by one process, one queue and one
worker (FR-U05), so the check reads the undo informer's cache that reconciling undos requires
anyway: no extra API call per pass, and no RBAC beyond the `workloadundos` access FR-U01 already
needs. The rejection message names the undo object and the overlapping namespace.

**The stall, stated rather than hidden.** A U that never reaches `Applied` — one target
permanently blocked by BR-U04, holding it in `PartiallyApplied` — blocks hardening of those
namespaces until someone deletes it. Accepted: the rejection names the object, deleting it is
one command, and a live undo the operator has not resolved is a poor moment to start hardening
the same namespaces.

Deliberately **not** 001's BR-03 exclusivity. Existence is not the test: an undo that reached
`Applied` sits in the cluster forever with no finalizer to clean it up, and using its presence as
a lock would turn a finished request into permanent policy over those namespaces.

**All of this lives in 002, and none of it in the undo controller.** It is the only amendment to
an implemented spec this document asks for — one check in `HardeningReconciler.validate`, one
error case, one acceptance criterion.

## Functional requirements

### FR-U01 — Interface

A second namespaced CRD: group `hardening.acme.corp`, version `v1alpha1`, kind **`WorkloadUndo`**.

A new kind rather than a field on `WorkloadHardening`, because BR-U01's whole argument is that
an undo does not need the original object and must work after it is deleted. A `mode` field on
the existing kind would make the immutable-spec rule (BR-07) mean two different things and
require the object to survive to be useful.

```yaml
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadUndo
metadata:
  name: tenant-rollback
  namespace: isolation-system
spec:
  namespaces: [tenant-a, tenant-b]
  exempt: false # optional; default false. Annotate reverted targets skip=true (BR-U09)
  approvedPlan: [] # empty => preview only; the only mutable field
```

`namespaces` holds 1–16 unique DNS labels. There is no policy block: what to remove is on the
workloads. `approvedPlan` holds up to 128 twelve-character hex hashes and is the only mutable
field, by the same CEL transition rule as 001 and 002 — `exempt` is immutable with the rest,
because it changes what the approved hashes describe.

### FR-U02 — The inverse plan

A pure function, `plan.Invert(pod *corev1.PodSpec, provenance string, req Request) → Plan`,
returning the same `Plan` type: deletions in `Changes`, everything skipped in `Findings`. It
parses the annotation, compares each record against the live template (BR-U02), maps records to
deletion paths (BR-U03), and drops what admission gates (BR-U04).

`Request` is `plan.Policy` under the name the refactor above gives it: the per-namespace value
`HardeningReconciler.validate` already builds, today carrying `Coverage` from the namespace's
LimitRanges, extended with the `enforce` label. Same shape, same construction point, so the
cluster reads stay in the controller and the decision stays pure. If the refactor is skipped,
the argument is `plan.Policy` and nothing else changes.

**Reused as-is:** `Canonical`, `Hash`, `Lines`, `Provenance` and `Patch`, which already renders
`null` values and sets the annotation in the same document. **New:** `Invert` and the record
parser. `plan.Build` is *not* reused — it takes a policy and finds gaps, which is a different
question from the same package.

### FR-U03 — Preview

Every changed target's deletion patch is issued with `DryRun: [All]`, under FR-03's rules
unchanged: cached while unarmed, always re-run on apply, a refusal reported as the target's
outcome without requeueing a preview.

Warning headers from the dry-run response are captured through the client's warning handler and
reported as findings on that target (BR-U04). What the dry-run does not cover is BR-U04's first
two paragraphs, which is why the `enforce` and LimitRange checks are not optional.

Status renders, per target: the object reference, the change hash, the paths that would be
deleted with the values being removed, the records left in place with the reason, the pods
affected and the rollout mechanism.

### FR-U04 — Apply

FR-04 unchanged: deterministic order, the annotation rewrite in the same request (BR-U05), keep
what succeeded on failure, report `PartiallyApplied`, return an error so the queue retries,
never roll back. The retry recomputes, and a target whose records are gone has nothing left to
delete, so convergence needs no bookkeeping — the mirror of BR-01 making an apply idempotent.

### FR-U05 — Reconciliation

FR-05 unchanged, on the same binary, queue and worker, with `WorkloadUndo` as a third watched
resource. No finalizer, no workload informer, no drift repair. `Applied` is terminal until
`approvedPlan` moves; `Rejected` is re-evaluated every resync.

### FR-U06 — Status

FR-06's phases unchanged — `Pending`, `Rejected`, `Previewed`, `Applied`, `PartiallyApplied` —
because the phase describes the disposition of the **request**, not the direction of the change,
and sharing the vocabulary shares the rendering code. The per-target outcome replaces `Patched`
with **`Reverted`**, which is the one place the direction is visible and the one place a wrong
word would mislead.

An undo whose plan is **empty** is `Applied`, not `Previewed`: there is nothing to approve and
the request is complete, and BR-U10 makes the difference load-bearing rather than cosmetic —
anything short of `Applied` holds hardening off those namespaces.

New finding reasons: `EditedSinceHardening`, `BlockedByPodSecurity`, `BlockedByLimitRange`,
`NoRecord`. A `Reverted` row is retained across later passes for FR-06's reason: the target
drops out of the recomputed plan once its annotation is gone.

## Non-functional requirements

- **NFR-U01 — Simplicity.** No new dependencies. One new pure function, one new CRD, one new
  reconciler sharing the existing queue.
- **NFR-U02 — Safety.** No deletion of a path not recorded by this tool. No deletion of a value a
  human changed. No deletion admission would make unschedulable. No write before a dry-run of
  that same patch in the same pass. No patch without its annotation rewrite in the same request.
- **NFR-U03 — Access.** **No new verbs on any core resource.** NFR-03 already grants
  `deployments`, `statefulsets`, `daemonsets` get/list/patch cluster-wide and `namespaces`,
  `limitranges` get/list. The delta is the `workloadundos` resource and its status.
- **NFR-U04 — Verification.** Every acceptance criterion has an automated test, 90% unit
  coverage per `AGENTS.md`. AC-U04, AC-U05 and AC-U08 are script-only: a fake client runs neither
  admission plugin nor the Deployment controller.

## Acceptance criteria

| ID     | Scenario                                                                                                                                                                                | Evidence |
| ------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| AC-U01 | A workload hardened by 002 and then undone retains no field this tool wrote — modulo the empty parent objects BR-08 accepts — and the `filled` annotation is gone                      | Unit     |
| AC-U02 | A record whose live value was edited by a human is left alone and reported; the comparison is on the rendered form, so `capabilities.drop` recorded as `[ALL]` matches a live `["ALL"]`  | Unit     |
| AC-U03 | **The seccomp case:** the deletion patch names `securityContext.seccompProfile`, not `.type`; the resulting template has no `seccompProfile` key and is accepted by a dry-run            | Unit     |
| AC-U04 | **Pod Security:** in `enforce: restricted` the four always-on fields are skipped and reported while requests are still removed; in `enforce: baseline` all of them are removed          | Script   |
| AC-U05 | **LimitRange:** a `min` with no default blocks removal of that request and reports it; a LimitRange supplying a default has no record to undo in the first place                         | Script   |
| AC-U06 | Partial undo rewrites the annotation to exactly the surviving records, in the same request; a fully undone target has the annotation removed                                            | Unit     |
| AC-U07 | A `paused`, `OnDelete` or `partition > 0` target is refused with its remedy named; a protected namespace rejects the object; a `skip`-annotated target **is** undone                     | Unit     |
| AC-U08 | A Deployment's ReplicaSet carries the copied `filled` annotation and is never targeted                                                                                                  | Script   |
| AC-U09 | Preview writes nothing, publishes a hash per target, and a target whose recorded field a human edits between preview and apply is `Stale`                                               | Unit     |
| AC-U10 | A workload deleted and recreated after hardening carries no annotation and yields no target                                                                                             | Unit     |
| AC-U11 | On kind: harden a Deployment, approve, apply, undo, approve, apply — the rollout completes twice, the pods stay Ready, and QoS returns to `BestEffort`                                  | Script   |
| AC-U12 | The CRD installs and the API server rejects an empty namespace list and an edit to any field but `approvedPlan`, `exempt` included                                                     | Script   |
| AC-U13 | **No silent re-harden:** after a full undo, an `Applied` WorkloadHardening whose generation has not moved issues no API calls; editing its `approvedPlan` republishes the undone target with a new hash as `Unapproved`, and does not patch it | Unit     |
| AC-U14 | `exempt: true` writes `hardening.acme.corp/skip` in the same patch as the revert, and a later WorkloadHardening reports that target excluded rather than planning it; `exempt: false` leaves no annotation behind | Unit     |
| AC-U15 | **Overlap:** a WorkloadHardening is `Rejected` while a non-`Applied` WorkloadUndo names one of its namespaces, and reaches `Previewed` on the resync after that undo is `Applied`; an undo naming a namespace a live hardening also names is **not** rejected; two objects created together do not deadlock | Unit     |
| AC-U16 | An undo whose plan is empty reaches `Applied` rather than `Previewed`, and does not hold off a hardening of the same namespaces | Unit     |

**AC-U03 and AC-U04 are the two most easily got wrong**, and they fail in opposite ways. AC-U03
fails at the dry-run, loudly, on every target — an undo that never works. AC-U04 passes the
dry-run cleanly and halts the rollout afterwards, which is 002's AC-05 trap repeated at
namespace scope.

## Error cases

| Case                                                          | Behaviour                                                                                                     |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| No workload in any named namespace carries the annotation     | `Applied` immediately, with a message saying so; not an error. **Terminal on purpose:** `Previewed` is not `Applied`, so an empty undo left sitting in `Previewed` would block hardening of those namespaces forever under BR-U10 |
| A record's live value was edited                              | Skipped, reported `EditedSinceHardening`, left in the rewritten annotation                                      |
| The namespace enforces `restricted`                           | The four PSS fields skipped and reported; requests still removed; both recorded in the rewritten annotation     |
| A LimitRange `min` with no default covers a recorded request  | That request skipped, reported `BlockedByLimitRange`                                                            |
| The annotation is malformed or unparseable                    | That target is a finding naming the object; other targets are unaffected. Never guessed at                     |
| A recorded path is already absent from the template           | Nothing to delete; the record is dropped from the rewritten annotation, reported `NoRecord`                     |
| The dry-run refuses the deletion patch                        | That target's outcome records the API server's message; other targets unaffected                                |
| A dry-run warning names a Pod Security violation              | Reported as a finding on that target alongside whatever the `enforce` check decided                             |
| The undo's own rollout halts                                  | Not detected, as in 002's G-02. `Applied` means the API server accepted the patch                               |
| The workload was patched by two `WorkloadHardening` objects   | Only the second object's records exist to undo. G-U01                                                           |
| A non-`Applied` `WorkloadHardening` names an overlapping namespace | `Rejected`, naming the object and the namespace. Non-terminal, so it clears itself once that object is `Applied` (BR-U10) |
| A hardening object's `approvedPlan` is edited after an undo   | The undone target reappears in its plan with a new hash, as `Unapproved`. Not patched, but re-approvable — BR-U09, and the reason `exempt` exists |

## Out of scope

### Declined

| #     | Excluded                                                       | Why                                                                                                                                                                  |
| ----- | -------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D-U01 | Restoring a previous value                                     | BR-01 means there was never one. An undo is a deletion, and anything else would be a snapshot feature.                                                               |
| D-U02 | Undoing through the original `WorkloadHardening` object        | BR-U01. The record outlives the object on purpose; making the undo need it throws that away.                                                                         |
| D-U03 | Modelling admission beyond namespace labels and LimitRanges    | Cluster-wide Pod Security defaults and third-party policy engines are unbounded. The dry-run warning (FR-U03) catches what it catches; the rest halts a rollout, which is the pre-existing failure mode. |
| D-U04 | Forcing a removal past a Pod Security refusal                  | The operator's remedy is to relabel the namespace or edit the workload, both of which are decisions this tool should not make on their behalf.                       |
| D-U05 | Undoing a hardening this tool did not perform                  | No annotation, no record, no evidence of what was there before.                                                                                                      |

### Deferred

| #     | Not yet done                                     | Interim position                                                                                                                                                  |
| ----- | ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| G-U01 | A workload patched by two objects in sequence    | The second annotation write replaces the first, so object 1's record is lost — 002's G-03. 001 refuses this per namespace (its BR-03); 002 does not. Undo inherits the gap rather than creating it. |
| G-U02 | Watching the undo's rollout to completion        | Shared with 002's G-02, and sharper here: an undo is usually run *because* a rollout halted.                                                                      |
| G-U03 | Undoing a subset of fields rather than all       | The unit of approval is a target. Per-field approval would need a hash per field and a bigger `approvedPlan` than 128 entries allows.                             |

## Metrics endpoint

There is no `prometheus/client_golang` in `go.sum` and NFR-01 forbids new dependencies. A
one-shot controller has about six counters worth having — reconciles by phase, targets patched,
targets reverted, dry-run refusals, apply failures, queue depth — and the text exposition format
for six counters is `net/http`, `sync/atomic` and a `fmt.Fprintf` loop in roughly 40 lines, with
no `go.mod` change. That is the version to build. It closes 001's G-07 and 002's G-07.

A Grafana stack is a separate decision and a larger one: more manifest than controller, and it
scrapes numbers a `kubectl get` already shows for a tool that runs once per request. Build it
only if the deliverable is a screenshot, and then from a `hack/` script installing
`kube-prometheus-stack` rather than from checked-in dashboards. Not otherwise.

## Integration/e2e tests on kind

Most of this exists. `hack/verify-isolation.sh` already runs the full NetworkIsolation cycle on
a live cluster — TCP and UDP, over pod IP *and* ClusterIP, before isolation, after enforcement
converges and after the object is deleted, plus a kubelet-readiness check that catches a CNI
enforcing ingress for non-pod sources. `hack/verify-hardening.sh` and
`hack/verify-crd-hardening.sh` cover AC-15 and AC-16, including the limits-only Deployment, the
`default`-only LimitRange, the skip annotation and the provenance annotation.

The actual gap is 002's G-06, and it is narrower than "write integration tests":

1. **`.github/workflows/` exists and is empty — there is no CI at all.** Wire `make test`,
   `make verify`, `make verify-crd`, `make verify-hardening` and `make verify-crd-hardening`
   into one workflow on a kind cluster. This is the whole of the bonus for 001 and 002.
2. **`hack/verify-undo.sh`**, covering AC-U04, AC-U05, AC-U08 and AC-U11 — the four criteria a
   fake client cannot reach, because it runs neither admission plugin nor the Deployment
   controller.
3. **envtest** for the CEL transition rules and the structural schemas of all three CRDs, which
   the scripts cover only as far as `kubectl apply` reports.

Rewriting the existing scripts from a fresh test plan would rebuild working code.
