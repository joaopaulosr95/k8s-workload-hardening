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
- **Selected set:** the workloads an undo covers — the three kinds in its named namespaces whose
  **own** `metadata.labels` match `spec.workloadSelector`, before BR-U06's exclusions. It is the
  scope of the bypass, not just of the revert: a workload is in it whether or not it carries a
  record to undo.

### BR-U01 — The checkpoint is the annotation, not status

The provenance annotation on the patched workload (002's BR-08), never `status.applied[]`.
Status dies with the custom resource: one-shot semantics and no finalizer mean deleting the
`WorkloadHardening` destroys any record living there. The annotation outlives it, which is what
makes an undo a standalone operation — it needs namespaces and a selector, not the original
object.

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

002's BR-04 and BR-05 apply unchanged within the selected set: 1–16 explicit namespaces,
protected namespaces reject the whole object, the same three kinds, controlled objects skipped, and `spec.paused`, `OnDelete` or
`partition > 0` **refused**. An undo patch sits just as inert on a workload that will not roll
out, and the operator's remedy — unpause, or roll manually — is named in the refusal. (A halted
rollout is not a `paused` Deployment, so the case is rarer here than it reads.)

Two deltas:

- **`hardening.acme.corp/skip` is ignored when selecting.** A target carrying both `skip` and
  `filled` was hardened before someone asked to be left alone. Removing the hardening honours
  that intent rather than contradicting it. Not a contradiction with BR-U09, which *writes*
  `skip` after reverting: the annotation excludes a workload from **hardening**, and an undo
  reads `filled` to decide what to do.
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

### BR-U09 — A reverted target is held out of hardening, by the mechanism that already exists

An undone workload has absent effective values again, so BR-01 sees a genuine gap and 002 would
fill it. Nothing stops that on its own. FR-05's generation gate happens to prevent it today — an
`Applied` object issues no API calls at all (AC-14) — but that is a property of the *request's*
lifecycle, not a rule about the workload, and FR-01's batched approvals move the generation by
design. The moment anyone edits `approvedPlan`, the reverted target is back in the plan with a
fresh hash, one blanket copy-paste from being re-patched. A recreated object, or a second one
naming the namespace (G-03), gets there from `Pending`.

Left there, the two kinds form a loop: the undo reverts, hardening refills, the undo reverts
again, and every round trip rolls every pod of every affected target **twice**.

**So the revert patch also holds the target out of hardening, using 002's own exclusion.**
BR-04 already skips any workload annotated `hardening.acme.corp/skip: "true"` — "the escape
hatch for a workload that genuinely needs what the policy would take away", which a workload
someone had to revert has just demonstrated. Writing it closes the loop at the source: a
reverted target stops being a target, so there is nothing to refill and no second object to
coordinate with.

Two annotations, both written **in the same patch as the revert** (BR-U05 is already rewriting
`metadata.annotations` in that request, so this costs no extra API call and leaves no window):

| Annotation                        | Value                    | Purpose                                     |
| --------------------------------- | ------------------------ | ------------------------------------------- |
| `hardening.acme.corp/skip`        | `"true"`                 | What BR-04 already reads. **002 is unchanged** |
| `hardening.acme.corp/skip-by`     | the undo object's UID    | Who wrote it, so only the writer removes it |

The marker is not bookkeeping for its own sake. `skip` is single-valued and shared: without
knowing who set it, the release below would delete an exemption a human set by hand, which is
the precise harm BR-01 exists to prevent, committed by the feature built to respect it. With it,
the rule is BR-U02's discipline applied to one more field — **touch only what you wrote, and
only if it is unchanged.**

Three cases on write, decided from what is already on the target:

| Found on the target                  | Written                          | Why                                                     |
| ------------------------------------ | -------------------------------- | -------------------------------------------------------- |
| no `skip`                            | `skip: "true"` and `skip-by: me` | Nothing to preserve                                      |
| `skip` with another undo's `skip-by` | nothing — the object was already `Rejected` | One bypass per workload (BR-U11)             |
| `skip` with **no** `skip-by`         | neither                          | A human's exemption. Not ours to mark, and not ours to remove |

The marker therefore has exactly one owner for as long as it exists, which is what BR-U11 buys
and why that rule is worth its cost.

Written whenever **anything** was reverted on that target, including a partial revert where
BR-U04 gated the securityContext half. A target where nothing was reverted gets no patch and no
annotation, because there is nothing to hold. The per-target status row names the holder, so an
operator can see which object a `kubectl delete` would release.

### BR-U10 — Deleting the undo releases the target

The undo object carries a finalizer. On deletion it removes `skip` and `skip-by` from every
target whose `skip-by` still matches its own UID, then clears the finalizer. Deleting the undo
is therefore the gesture that makes a workload eligible for hardening again — one object, one
`kubectl delete`, no annotation editing across a namespace.

Left in place: a `skip` with no `skip-by`, which is a human's exemption and was never ours. A
marker naming another object cannot be reached, because BR-U11 refused the object before it
planned anything.

**A marker naming an object that no longer exists is not a claim.** A `skip-by` whose UID
matches no live `WorkloadHardeningUndo` — a finalizer force-cleared, an object removed while the
controller was down — is treated as unowned and taken by the next undo that selects the
workload. Without that, one orphaned annotation would block every future bypass of that workload
and the only remedy would be editing it by hand.

**This is the one finalizer in the project, and it is not the one 002 refused.** FR-05 rejected
a finalizer on `WorkloadHardening` because a delete would then hang on work that can fail:
undoing patches, halting rollouts, refusals from admission. This one removes two metadata keys.
It cannot be blocked by Pod Security or a LimitRange, neither of which reads `metadata`
annotations; it does not touch `spec.template`, so it starts no rollout; and where the workload
is gone there is nothing to clean and the finalizer clears. The residual cost is the honest one:
with the controller down, deleting a `WorkloadHardeningUndo` blocks until it is back.

**Nothing in 002 changes.** The earlier draft of this rule had the hardening loop read undo
objects and refuse whole namespaces — cross-object awareness, an amendment to an implemented
spec, and a deadlock when both kinds rejected each other. Per-target exclusion through an
annotation 002 already honours is smaller in every direction, and more precise: hardening still
proceeds on every workload in the namespace the undo did not touch.

There is no `spec.exempt` flag. A **permanent** exemption is a human writing `skip: "true"`
themselves, with no `skip-by`, which this feature then never removes. The two intents stay
distinct because the marker distinguishes them.

### BR-U11 — One bypass rule per workload

A `WorkloadHardeningUndo` is `Rejected` if any workload in its selected set already carries a
`skip-by` naming a different, live undo object. The message names the workload and the object
holding it; the remedy is to narrow the selector or delete the other rule.

This is a consequence of BR-U09 and BR-U10 rather than a separate policy. A bypass is a
**standing** claim, not a one-shot action: it persists for the object's whole lifetime and ends
when the object is deleted. Two standing claims over one workload cannot both be honoured by a
single-valued annotation, and any tie-break is wrong in one direction — releasing on the first
delete strips a bypass the surviving rule still asserts, releasing on the last leaves a workload
held by an object whose scope no longer covers it.

The reverting half would have been fine unrefereed, and an earlier draft of this document
declined the rule on exactly that ground: reverts are monotonic, both objects only remove, and
BR-U05 deletes each record as it is reverted, so the second finds only what the first could not
take. That reasoning is sound and no longer sufficient. It is an argument about the **action**,
and a selector-scoped bypass is judged by its **scope**.

The test is the annotation, not selector algebra. Two `matchLabels` sets can be compared for
satisfiability, but the question that matters is whether a claim exists on a workload that
exists, which is one list against the informer cache the controller already holds — no
intersection arithmetic, and no answer that depends on a workload that might be created later.

Refusal, not prevention at creation. A CEL rule cannot see other objects, and a validating
webhook is a deployment artifact with TLS and a CA bundle that 002 declined for a larger payoff
(D-01). `Rejected` is non-terminal (FR-05), so an object refused this way starts working by
itself once the conflicting rule is deleted, which is the behaviour an operator wants anyway.

Finer than 001's BR-03, deliberately: 001 refuses per namespace because a NetworkPolicy's effect
is namespace-wide, while a bypass names workloads. Two undos over one namespace are fine, and
splitting a large revert across objects by selector is the normal way to work within FR-U01's
caps.

## Functional requirements

### FR-U01 — Interface

A second namespaced CRD: group `hardening.acme.corp`, version `v1alpha1`, kind **`WorkloadHardeningUndo`**.

A new kind rather than a field on `WorkloadHardening`, because BR-U01's whole argument is that
an undo does not need the original object and must work after it is deleted. A `mode` field on
the existing kind would make the immutable-spec rule (BR-07) mean two different things and
require the object to survive to be useful.

```yaml
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardeningUndo
metadata:
  name: tenant-rollback
  namespace: isolation-system
spec:
  namespaces: [tenant-a, tenant-b]
  workloadSelector: # optional; absent selects every workload in those namespaces
    matchLabels: { app: api }
  approvedPlan: [] # empty => preview only; the only mutable field
```

`namespaces` holds 1–16 unique DNS labels. There is no policy block: what to remove is on the
workloads. `approvedPlan` holds up to 128 twelve-character hex hashes and is the only mutable
field, by the same CEL transition rule as 001 and 002 — `workloadSelector` is immutable with the
rest, because widening it after approval would change which workloads the approved hashes
describe and, more to the point, which workloads the object claims.

`workloadSelector` holds 1–8 `matchLabels` entries, as 001's `podSelector` does, and
`matchExpressions` is likewise not served. It matches the **workload object's own**
`metadata.labels` — not the pod template's, which is the same word meaning a different thing one
level down and the mistake this field is most likely to invite. `kubectl get deploy
--show-labels` is what an operator reads to predict it.

**Named `workloadSelector`, not `podSelector`.** 001 selects pods, because a NetworkPolicy's
subject is a pod. This selects the objects being patched. Reusing the name across two kinds that
select different things is exactly the collision the Refactor section is about.

Absent selects every workload in the named namespaces, which is the whole-namespace revert of a
bad hardening run and remains the common case. Present, the object is a bypass rule for the
workloads it names, and BR-U11 keeps it the only one covering them.

The object is kept after it reaches `Applied`, not cleaned up: while it exists its selected set
stays held out of hardening (BR-U09), and deleting it is what releases them (BR-U10).

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

FR-04 unchanged: deterministic order, the annotation rewrite and the two hold annotations in the
same request (BR-U05, BR-U09), keep
what succeeded on failure, report `PartiallyApplied`, return an error so the queue retries,
never roll back. The retry recomputes, and a target whose records are gone has nothing left to
delete, so convergence needs no bookkeeping — the mirror of BR-01 making an apply idempotent.

### FR-U05 — Reconciliation

FR-05 unchanged, on the same binary, queue and worker, with `WorkloadHardeningUndo` as a third
watched resource. No workload informer, no drift repair. `Applied` is terminal until
`approvedPlan` moves; `Rejected` is re-evaluated every resync.

One finalizer, for the release in BR-U10 and nothing else. The single worker
(`controller.go:41`) means a hardening reconcile and an undo reconcile never interleave, so the
hold is in place before any pass could act on the reopened gaps — and it would be anyway, since
the revert and the annotations are one API call.

### FR-U06 — Status

FR-06's phases unchanged — `Pending`, `Rejected`, `Previewed`, `Applied`, `PartiallyApplied` —
because the phase describes the disposition of the **request**, not the direction of the change,
and sharing the vocabulary shares the rendering code. The per-target outcome replaces `Patched`
with **`Reverted`**, which is the one place the direction is visible and the one place a wrong
word would mislead.

An undo whose plan is **empty** is `Applied`, not `Previewed`: there is nothing to approve and
the request is complete. It holds nothing, so deleting it releases nothing.

New finding reasons: `EditedSinceHardening`, `BlockedByPodSecurity`, `BlockedByLimitRange`,
`NoRecord`. A `Reverted` row is retained across later passes for FR-06's reason: the target
drops out of the recomputed plan once its annotation is gone.

## Non-functional requirements

- **NFR-U01 — Simplicity.** No new dependencies. One new pure function, one new CRD, one new
  reconciler sharing the existing queue.
- **NFR-U02 — Safety.** No deletion of a path not recorded by this tool. No deletion of a value a
  human changed. No deletion admission would make unschedulable. No write before a dry-run of
  that same patch in the same pass. No patch without its annotation rewrite and its hold in the
  same request. No removal of a `skip` this tool did not write.
- **NFR-U03 — Access.** **No new verbs on any core resource.** NFR-03 already grants
  `deployments`, `statefulsets`, `daemonsets` get/list/patch cluster-wide and `namespaces`,
  `limitranges` get/list — the hold and the release are annotation patches on objects this tool
  may already patch. The delta is the `workloadhardeningundos` resource, its status, and
  `update` on it for the finalizer.
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
| AC-U12 | The CRD installs and the API server rejects an empty namespace list, a `matchExpressions` selector, and an edit to any field but `approvedPlan` — `workloadSelector` included                | Script   |
| AC-U13 | **No silent re-harden:** after a full undo, an `Applied` WorkloadHardening whose generation has not moved issues no API calls; editing its `approvedPlan` republishes the undone target with a new hash as `Unapproved`, and does not patch it | Unit     |
| AC-U14 | **The hold:** the revert patch carries `skip: "true"` and `skip-by: <uid>` in the same request, and a later WorkloadHardening reports that target excluded rather than planning it — with no change to 002 | Unit     |
| AC-U15 | **The release:** deleting the undo removes both annotations and the finalizer; a `skip` with no `skip-by` keeps both, so a human's hand-set exemption survives an undo's whole lifecycle | Unit     |
| AC-U18 | **Exclusivity:** an undo whose selected set includes a workload already carrying another live object's `skip-by` is `Rejected`, naming both; it reaches `Previewed` on the resync after that object is deleted; a `skip-by` naming no live object does not block it | Unit     |
| AC-U19 | **Selector:** `workloadSelector` matches the workload's own labels and not the pod template's; two undos over one namespace with disjoint selectors both reach `Applied`; an absent selector covers every workload in the namespace | Unit     |
| AC-U20 | A workload carrying a human's bare `skip` is reverted but never marked, and the release leaves it alone | Unit     |
| AC-U16 | **No loop:** a hardening and an undo both naming a namespace converge — the reverted target is excluded from the next hardening plan and neither object churns its hashes across resyncs | Unit     |
| AC-U17 | A partially reverted target, where BR-U04 gated the securityContext half, is still held; a target where nothing was reverted gets no patch and no annotations | Unit     |

**AC-U03 and AC-U04 are the two most easily got wrong**, and they fail in opposite ways. AC-U03
fails at the dry-run, loudly, on every target — an undo that never works. AC-U04 passes the
dry-run cleanly and halts the rollout afterwards, which is 002's AC-05 trap repeated at
namespace scope.

## Error cases

| Case                                                          | Behaviour                                                                                                     |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| No workload in any named namespace carries the annotation     | `Applied` immediately, with a message saying so; not an error. Nothing was reverted, so nothing is held and nothing is released on delete |
| A record's live value was edited                              | Skipped, reported `EditedSinceHardening`, left in the rewritten annotation                                      |
| The namespace enforces `restricted`                           | The four PSS fields skipped and reported; requests still removed; both recorded in the rewritten annotation     |
| A LimitRange `min` with no default covers a recorded request  | That request skipped, reported `BlockedByLimitRange`                                                            |
| The annotation is malformed or unparseable                    | That target is a finding naming the object; other targets are unaffected. Never guessed at                     |
| A recorded path is already absent from the template           | Nothing to delete; the record is dropped from the rewritten annotation, reported `NoRecord`                     |
| The dry-run refuses the deletion patch                        | That target's outcome records the API server's message; other targets unaffected                                |
| A dry-run warning names a Pod Security violation              | Reported as a finding on that target alongside whatever the `enforce` check decided                             |
| The undo's own rollout halts                                  | Not detected, as in 002's G-02. `Applied` means the API server accepted the patch                               |
| The workload was patched by two `WorkloadHardening` objects   | Only the second object's records exist to undo. G-U01                                                           |
| A hardening object's `approvedPlan` is edited after an undo   | The reverted target is excluded by BR-04's skip annotation, so it is absent from the recomputed plan and reported as excluded (BR-U09) |
| The `skip` annotation was set by a human before the undo ran  | `skip-by` is absent, so the release leaves both the annotation and the exemption alone (BR-U10). The undo still reverts the fields it recorded |
| A selected workload is already claimed by another live undo   | `Rejected`, naming the workload and the holding object (BR-U11). Non-terminal, so narrowing the selector or deleting the other rule clears it |
| A `skip-by` names a UID with no live object                   | Treated as unowned and claimed by the next undo that selects the workload (BR-U10) |
| `workloadSelector` matches nothing in a named namespace       | Not an error. An empty selected set contributes no targets; an object whose whole plan is empty is `Applied` |
| A target is deleted while an undo holding it still exists     | Nothing to release; the finalizer clears on the next pass and does not block the delete |
| The controller is down when an undo is deleted                | The delete blocks on the finalizer until the controller returns. The one place in the project where an object waits on this controller, and it waits on removing two annotations (BR-U10) |

## Out of scope

### Declined

| #     | Excluded                                                       | Why                                                                                                                                                                  |
| ----- | -------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D-U01 | Restoring a previous value                                     | BR-01 means there was never one. An undo is a deletion, and anything else would be a snapshot feature.                                                               |
| D-U02 | Undoing through the original `WorkloadHardening` object        | BR-U01. The record outlives the object on purpose; making the undo need it throws that away.                                                                         |
| D-U03 | Modelling admission beyond namespace labels and LimitRanges    | Cluster-wide Pod Security defaults and third-party policy engines are unbounded. The dry-run warning (FR-U03) catches what it catches; the rest halts a rollout, which is the pre-existing failure mode. |
| D-U04 | Forcing a removal past a Pod Security refusal                  | The operator's remedy is to relabel the namespace or edit the workload, both of which are decisions this tool should not make on their behalf.                       |
| D-U05 | Undoing a hardening this tool did not perform                  | No annotation, no record, no evidence of what was there before.                                                                                                      |
| D-U06 | `matchExpressions`, and selecting by namespace label           | 001 serves neither, for the same reason: `matchLabels` covers the cases the brief describes and keeps the blast radius readable in the object. BR-U11's check reads annotations rather than comparing selectors, so a richer selector would not have made it harder — it is simply not needed. |

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
