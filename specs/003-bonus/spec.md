---
name: Bonus
description: |
  Undo for workload hardening, a metrics endpoint, integration/e2e tests on kind, a naming and
  documentation refactor across 001 and 002, and Diátaxis documentation for the whole project.
  Undo is specified in full; the other four are scoping notes.
author: João Bastos <joaopaulosr95@gmail.com>
status: Draft
# Flip to Approved once all five items have landed. 001 and 002 are Approved;
# this one stays Draft while any of its five is unbuilt, so the status answers
# "is this spec describing shipped behaviour?" rather than "has anyone read it".
#   [x] Refactor            — specs/003-bonus/refactor/tasks.md, merged
#   [ ] Undo                — specs/003-bonus/undo/tasks.md
#   [ ] Integration tests   — specs/003-bonus/integration-tests/tasks.md
#   [ ] Metrics endpoint    — specs/003-bonus/metrics/tasks.md
#   [ ] Documentation       — specs/003-bonus/docs/tasks.md
relatedResources:
  - specs/001-network-isolation/spec.md
  - specs/002-workload-hardening/spec.md
---

# Bonus

## Objective

Five items outside the two core tasks: a refactor, an undo for workload hardening, a metrics
endpoint, integration/e2e tests on kind, and documentation for the whole project.

## Context

Only one of the five is a feature. **Undo** writes to workloads other teams own, has a failure
mode that halts rollouts, and depends on an admission behaviour no dry-run reports — so it
carries business rules, acceptance criteria and error cases of its own, below. The other four
change how existing work is named, packaged, run or explained; they are scoped in a paragraph
each and deliberately not given the apparatus of a spec they do not need.

002's D-03 and G-01 point here for the undo, and BR-08 exists to make it possible. This
document is what those references resolve to.

## Refactor

Isolation was built under the assumption nothing else would exist in the project, so its types
took the unqualified names and hardening got the prefixed ones. The result is four collisions:

| Collision                                                                                  | Where                                     |
| ------------------------------------------------------------------------------------------ | ----------------------------------------- |
| `v1alpha1.Spec` / `Status` (isolation, unqualified) vs `HardeningSpec` / `HardeningStatus` | `types.go:58,71` vs `hardening.go:78,127` |
| `plan.Finding` vs `v1alpha1.Finding`                                                       | `plan.go:68`, `hardening.go:117`          |
| `Reconciler` vs `HardeningReconciler`                                                      | `reconcile.go:29`, `targets.go:33`        |
| **`plan.Policy` vs package `pkg/policy`** — unrelated things sharing a word                | `plan.go:83`, `pkg/policy`                |

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
inside `## Setup`, and lines 241–340 duplicate the structure a second time (Decisions and
Limitations at 269/306 mirroring 101/169). Merge per topic, with both features in each
section. One pass, one decision.

Operator-facing text counts as docs here: `deploy/crd-hardening.yaml` describes the kind with
"There is no undo.", which `kubectl explain` prints and which this feature makes false. It goes
in the same pass.

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
`seccompProfile: {}` fails API validation — and 002's BR-02 writes `seccompProfile` wherever the
pod-level value is absent and at least one container does not declare its own, which is nearly
every hardened target. Since a target yields exactly one strategic merge patch (`plan.Patch`),
that one field would take the whole target's undo down with it, requests included. The dry-run
refuses it, so it fails loudly rather than corrupting anything; it also fails every time.

| Record                                   | Deletion path                       | Why                                                                     |
| ---------------------------------------- | ----------------------------------- | ----------------------------------------------------------------------- |
| `...securityContext.seccompProfile.type` | `...securityContext.seccompProfile` | `type` is required; the empty parent is invalid                         |
| everything else                          | the leaf itself                     | `securityContext: {}`, `capabilities: {}`, `requests: {}` are all legal |

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

**A free second signal.** Pod Security's `warn` and `audit` modes _do_ evaluate workload
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
  that intent rather than contradicting it. Not a contradiction with BR-U09, which _writes_
  `skip` after reverting: the annotation excludes a workload from **hardening**, and an undo
  reads `filled` to decide what to do.
- **A ReplicaSet may carry the annotation, and soon three of them.** The Deployment controller
  copies a Deployment's own annotations onto the ReplicaSets it creates, so old ReplicaSets carry
  stale `filled` records — and, once BR-U09 writes them, stale `skip` and `skip-by` too. What
  keeps a ReplicaSet out is not BR-04's controlled-object exclusion but the kind list above:
  discovery builds targets from Deployments, StatefulSets and DaemonSets, and reads ReplicaSets
  only to report them. So the rule BR-U11 depends on is stated here rather than assumed —
  **`skip-by` is read from the three kinds, never from whatever happens to carry it** — because a
  copied marker names a live object and would otherwise reject an undo on behalf of a ReplicaSet
  nobody can hold. AC-U08 pins it on a live cluster, because a fake client does not run the
  Deployment controller.

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

### BR-U09 — The selected set is held out of hardening, by the mechanism that already exists

An undone workload has absent effective values again, so BR-01 sees a genuine gap and 002 would
fill it. Nothing stops that on its own. FR-05's generation gate happens to prevent it today — an
`Applied` object issues no writes at all (AC-14) — but that is a property of the _request's_
lifecycle, not a rule about the workload, and FR-01's batched approvals move the generation by
design. The moment anyone edits `approvedPlan`, the reverted target is back in the plan with a
fresh hash, one blanket copy-paste from being re-patched. A recreated object, or a second one
naming the namespace (G-03), gets there from `Pending`.

Left there, the two kinds form a loop: the undo reverts, hardening refills, the undo reverts
again, and every round trip rolls every pod of every affected target **twice**.

**So the object holds its selected set out of hardening, using 002's own exclusion.** BR-04
already skips any workload annotated `hardening.acme.corp/skip: "true"` — "the escape hatch for
a workload that genuinely needs what the policy would take away", which a workload someone had
to revert has just demonstrated. Writing it closes the loop at the source: a held workload stops
being a target, so there is nothing to refill and no second object to coordinate with.

**The hold covers everything selected, not only what was reverted.** The object is a standing
bypass over the workloads its selector names, and reverting is what it does to those of them
that carry records. A selected workload with nothing to undo is held too — that is the
difference between a bypass rule and a one-shot revert, and it is why BR-U11 protects a real
claim rather than a technicality. The cost is stated rather than buried: a broad selector
exempts workloads from future hardening that were never hardened, so FR-U03's preview renders
the **held** set and not only the reverted one.

Two annotations, both written **in the same patch as the revert** (BR-U05 is already rewriting
`metadata.annotations` in that request, so this costs no extra API call and leaves no window):

| Annotation                    | Value                 | Purpose                                        |
| ----------------------------- | --------------------- | ---------------------------------------------- |
| `hardening.acme.corp/skip`    | `"true"`              | What BR-04 already reads. **002 is unchanged** |
| `hardening.acme.corp/skip-by` | the undo object's UID | Who wrote it, so only the writer removes it    |

The marker is not bookkeeping for its own sake. `skip` is single-valued and shared: without
knowing who set it, the release below would delete an exemption a human set by hand, which is
the precise harm BR-01 exists to prevent, committed by the feature built to respect it. With it,
the rule is BR-U02's discipline applied to one more field — **touch only what you wrote, and
only if it is unchanged.**

Four cases on write, decided from what is already on the target:

| Found on the target                  | Written                                     | Why                                                                                                                                             |
| ------------------------------------ | ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| no `skip`                            | `skip: "true"` and `skip-by: me`            | Nothing to preserve                                                                                                                             |
| `skip` with **my own** `skip-by`     | nothing                                     | Already held. The steady state, and what makes a resync of an intact object issue no writes (AC-U22)                                            |
| `skip` with another undo's `skip-by` | nothing — the object was already `Rejected` | One bypass per workload (BR-U11)                                                                                                                |
| `skip` with **no** `skip-by`         | neither                                     | A human's exemption — including a marker whose `skip-by` someone stripped, which is indistinguishable from one. Not ours to mark, nor to remove |

The marker therefore has exactly one owner for as long as it exists, which is what BR-U11 buys
and why that rule is worth its cost.

**A hold is not gated by `approvedPlan`, and a revert is.** BR-07's approval gate exists
because a template patch restarts every pod of the workload (BR-09). The hold writes two keys
under `metadata.annotations`, never touches `spec.template`, and so changes no pod-template hash
and starts no rollout. Making an operator copy a hash per workload to achieve nothing but an
exemption would be friction with no risk behind it.

The consequence is deliberate and is this feature's one divergence from FR-03's "preview writes
nothing": **an unarmed undo already holds its selected set.** That is the right order for an
incident — the bypass stops hardening touching those workloads the moment the object is
admitted, and the operator then decides at leisure whether to also roll the changes back. The
hold patch is still dry-run first, like every other write (NFR-U02).

So there are three patch shapes, and arming decides between them as much as records do. An
**armed** object's selected workload with records yields one patch carrying the revert, the
rewritten `filled` annotation and the hold. A workload with no records yields a metadata-only
hold patch — and so does every workload of an **unarmed** object, which is the paragraph above
made concrete. The per-target status row names the holder, so an operator can see which object a
`kubectl delete` would release.

**The hold is re-asserted on every resync.** Nothing else would keep it: an `Applied` object is
otherwise inert, so a `skip` someone strips by hand would silently lapse and the next hardening
would pick the workload up. This is not D-02's drift repair: that rule is about fields the tool
does not own, and this annotation is the object's own bookkeeping.

**What that costs is reads, and there is no cache to take them from.** FR-U05 keeps 002's refusal
of a workload informer, so the comparison is the discovery pass this feature already runs — a
`List` per kind per namespace against the API server, up to forty-eight of them for an object
that has finished reverting, on every resync, for as long as it exists. The lists are not extra
within a pass; what is new is that an `Applied` undo keeps having passes, where an `Applied`
`WorkloadHardening` stops (FR-U05). That is the price of a bypass that cannot silently lapse, and
it is paid entirely in reads: an object whose holds are all intact issues no **writes**. AC-14's
guarantee is restated on that narrower word throughout (AC-U13, AC-U22), which is the honest
version of it in any case — FR-05 already reads the request object itself on every resync.

**Holds stand while the object is `Rejected`.** The claim belongs to the object, not to its
phase, and an object refused for a missing namespace has not stopped asserting a bypass it
already wrote.

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

**This is not the finalizer 002 refused, and it is the cheaper of the two the project will then
carry.** 001 already has one — `hardening.acme.corp/cleanup`, which deletes NetworkPolicies on
the way out and retains itself when that fails. FR-05 refused a _second_ one on
`WorkloadHardening` because a delete would then hang on work that can fail: undoing patches,
halting rollouts, refusals from admission. This one removes two metadata keys.
It cannot be blocked by Pod Security or a LimitRange, neither of which reads `metadata`
annotations; it does not touch `spec.template`, so it starts no rollout; and where the workload
is gone there is nothing to clean and the finalizer clears. The residual cost is the honest one:
with the controller down, deleting a `WorkloadHardeningUndo` blocks until it is back — which is
already true of a `NetworkIsolation`, waiting on strictly more.

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
exists — which is the selected set this pass has already listed, read for one annotation, plus a
single cluster-wide list of `WorkloadHardeningUndo` to tell a live holder from a dead UID. No
intersection arithmetic, and no answer that depends on a workload that might be created later.

**Two undos can both preview the same workload.** Nothing is claimed until one of them is
admitted and writes the hold, so BR-U11 cannot fire earlier. The loser flips from `Previewed` to
`Rejected` on a later pass, possibly after its operator has already copied hashes into
`approvedPlan`. That is correct — the conflict is real and only became visible then — and the
rejection names the winner, so the remedy is the one it would always have been.

Refusal, not prevention at creation. A CEL rule cannot see other objects, and a validating
webhook is a deployment artifact with TLS and a CA bundle that 002 declined for a larger payoff
(D-01). `Rejected` is non-terminal (FR-05), so an object refused this way starts working by
itself once the conflicting rule is deleted, which is the behaviour an operator wants anyway.

Finer than 001's BR-03, deliberately: 001 refuses per namespace because a NetworkPolicy's effect
is namespace-wide, while a bypass names workloads. Two undos over one namespace are fine, and
splitting a large revert across objects by selector is the normal way to work within FR-U01's
caps.

**The cost lands on FR-U01's common case, and is not hidden.** An undo with no selector claims
every workload in its namespaces for its whole lifetime, so the next undo naming any of them is
`Rejected` — and both remedies this rule offers, narrowing a selector and splitting by selector,
are unavailable while that object stands. Deleting it is the remedy, and deleting it is also what
releases the bypass, which is the point rather than a wrinkle: a standing claim over whole
namespaces is exactly what an operator asked for by omitting the selector. One who means to
revert in pieces names a selector on the first object too. The rejection names the holder, so the
choice is visible at the moment it has to be made rather than inferred from annotations.

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
field: `workloadSelector` is immutable with `namespaces`, because widening it after approval
would change which workloads the approved hashes describe and, more to the point, which workloads
the object claims.

The transition rule is 001's and 002's in shape but not in text, and the difference is not
cosmetic. 002 compares its three fields with no `has()` guard and the CRD says why: all three are
required or defaulted, so all three are always present. `workloadSelector` is optional, so the
same expression raises a CEL runtime error on an object that omits it — and a transition rule
that errors rejects the update, so an unguarded copy would refuse **every** edit to a
whole-namespace undo, the `approvedPlan` edit that arms it included. The rule is therefore
`has(self.workloadSelector) == has(oldSelf.workloadSelector) && (!has(self.workloadSelector) ||
self.workloadSelector == oldSelf.workloadSelector)`, and AC-U12 covers it.

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

```go
func Invert(pod *corev1.PodSpec, provenance string, req Request) (Plan, string)
```

A pure function, returning deletions in `Changes`, everything skipped in `Findings`, and the value
the provenance annotation should carry afterwards — `""` when nothing survives and the annotation
is removed entirely. It
parses the annotation, compares each record against the live template (BR-U02), maps records to
deletion paths (BR-U03), and drops what admission gates (BR-U04).

**The second result is load-bearing, not decoration.** BR-U05 rewrites the annotation in the same
request as the deletions; `Plan` carries only `Changes` and `Findings`; and a `Finding` has no
path. So the records that survived cannot be recovered from what `Plan` holds. The alternative is
the controller reverse-mapping deletion paths back to records through BR-U03's table — which is
this function's job, and reading a leaf back out of `securityContext.seccompProfile` is guessing.

`Request` is `plan.Policy` under the name the refactor above gives it: the per-namespace value
`HardeningReconciler.validate` already builds, today carrying `Coverage` from the namespace's
LimitRanges, extended with the `enforce` label. Same shape, same construction point, so the
cluster reads stay in the controller and the decision stays pure. If the refactor is skipped,
the argument is `plan.Policy` and nothing else changes.

**Reused as-is:** `Canonical`, `Hash`, `Lines` and `Provenance`. A `Change` whose `JSON` is nil
already marshals to `null` through `Patch`'s path walker, so the deletion body itself costs
nothing new.

**Changed: `Patch`.** It hardcodes the annotation it writes — `metadata.annotations[filled] =
Provenance(changes)` — which is right for 002 and wrong in every one of an undo's cases: `filled`
must carry the records that _survived_, or be removed with `null`, and `skip` and `skip-by` ride
in the same document (BR-U05, BR-U09). The annotations become a parameter, and 002 passes what
`Patch` computes for itself today.

**New: `Invert`, and the part it would be a mistake to call a parser.** Reading the annotation is
the easy half. Comparing a record against the live value (BR-U02) needs a reader that resolves a
recorded leaf path on the live `PodSpec` and renders what it finds in `Lines`' form — `[ALL]` for
a capability list, `RuntimeDefault` for a seccomp type, `Quantity.String()` for a request.
`plan.Build` cannot supply it: it renders only fields it is about to write, and an undo reads
fields that are already set. It is seven leaf shapes, so a switch is the right size — but a
renderer that drifts from `Build`'s produces a wrong answer rather than an error, silently
leaving a field in place as "edited by a human". So both directions read one table of leaf → read
→ render, and AC-U02 is the test that pins the two together.

`plan.Build` is _not_ reused for the plan itself either — it takes a policy and finds gaps, which
is a different question from the same package.

### FR-U03 — Preview

Every changed target's deletion patch is issued with `DryRun: [All]`, under FR-03's rules
unchanged: cached while unarmed, always re-run on apply, a refusal reported as the target's
outcome without requeueing a preview.

Warning headers from the dry-run response are reported as findings on that target (BR-U04).
client-go's warning handler is configured per `rest.Config`, not per request, and one clientset
serves every reconciler, so the capture is a sink the handler appends to and the caller drains
around each patch. That is correct only because the controller runs a single worker. It is
written down because the day a second worker looks attractive this is what breaks, and it breaks
by attributing one workload's warning to another rather than by failing.

What the dry-run does not cover is BR-U04's first two paragraphs, which is why the `enforce` and
LimitRange checks are not optional.

Status renders, per target: the object reference, the change hash, the paths that would be
deleted with the values being removed, the records left in place with the reason, the pods
affected and the rollout mechanism.

### FR-U04 — Apply

FR-04 unchanged: deterministic order, the annotation rewrite and the hold in the same request
(BR-U05, BR-U09), keep what succeeded on failure, report `PartiallyApplied`, return an error so
the queue retries, never roll back. The retry recomputes, and a target whose records are gone has nothing left to
delete, so convergence needs no bookkeeping — the mirror of BR-01 making an apply idempotent.

### FR-U05 — Reconciliation

FR-05's machinery is unchanged — the same binary, queue and worker, with `WorkloadHardeningUndo`
as a third watched resource, no workload informer and no drift repair — but **its terminality is
not**, and this is the one place the undo diverges from FR-05's lifecycle.

FR-05 returns from an `Applied` object before it reads anything. An undo cannot, because BR-U09's
hold has to be re-asserted or it silently lapses. So `Applied` is terminal for the **revert** —
the deletion plan is not recomputed and no `spec.template` is touched again until `approvedPlan`
moves — while the hold comparison runs on every pass, at the read cost BR-U09 prices. `Rejected`
is re-evaluated every resync as in FR-05, and holds already written stand throughout (BR-U09).

The divergence is the direct cost of BR-U09: a bypass that stops being asserted the moment its
object goes quiet is not a bypass. Everything else about the phase — that it is reached when the
approved reverts have landed, that it survives targets left `Unapproved` — is FR-06's, unchanged.

One finalizer on this kind, for the release in BR-U10 and nothing else. The single worker
(`controller.go:42`) means a hardening reconcile and an undo reconcile never interleave, so the
hold is in place before any pass could act on the reopened gaps — and it would be anyway, since
the revert and the annotations are one API call.

### FR-U06 — Status

FR-06's phases unchanged — `Pending`, `Rejected`, `Previewed`, `Applied`, `PartiallyApplied` —
because the phase describes the disposition of the **request**, not the direction of the change,
and sharing the vocabulary shares the rendering code. The per-target outcome replaces `Patched`
with **`Reverted`**, which is the one place the direction is visible and the one place a wrong
word would mislead, and adds **`Held`** for a selected workload carrying no record.

`Held` exists because BR-U09's bypass is wider than the revert: such a workload is held, so it
needs a row, and a row needs an outcome. `Outcome` carries no `omitempty`, so leaving it blank
would serialise as an empty string and read as a fault — 002's own `Planned` decision, one
feature along. The CRD declares `outcome` as a free string, so this costs vocabulary and no
schema change.

An undo whose **revert** plan is empty is `Applied`, not `Previewed`: there is nothing to
approve and the request is complete. It still holds its selected set (BR-U09), and deleting it
still releases them — an object that reverted nothing is a pure bypass rule, which is a
legitimate thing to create deliberately.

Status carries, per target, the holder of that workload's `skip-by` and whether it is this
object, so the bypass is readable from the object rather than inferred across annotations.

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
  may already patch, and BR-U04's `enforce` label sits on a namespace it already reads. The delta
  is the `workloadhardeningundos` resource, its status, `update` on it for the finalizer, and
  `workloadhardeningundos/finalizers: update`, which 001 already grants for its own finalizer and
  which `OwnerReferencesPermissionEnforcement` makes load-bearing where it is enabled.
- **NFR-U04 — Verification.** Every acceptance criterion has an automated test, 90% unit
  coverage per `AGENTS.md`. AC-U04, AC-U05, AC-U08, AC-U11 and AC-U12 are script-only, and
  AC-U03's second half with them: a fake client runs neither admission plugin, nor the Deployment
  controller, nor the CRD's own schema and CEL rules — and validates no API type at all, so a
  dry-run it accepts says nothing about whether the API server would accept the same body.

## Acceptance criteria

| ID     | Scenario                                                                                                                                                                                                                                                                                       | Evidence      |
| ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- |
| AC-U01 | A workload hardened by 002 and then undone retains no field this tool wrote — modulo the empty parent objects BR-08 accepts — and its `filled` annotation is replaced by `skip` and `skip-by` in the same patch                                                                                | Unit          |
| AC-U02 | A record whose live value was edited by a human is left alone and reported; the comparison is on the rendered form, so `capabilities.drop` recorded as `[ALL]` matches a live `["ALL"]`                                                                                                        | Unit          |
| AC-U03 | **The seccomp case:** the deletion patch names `securityContext.seccompProfile`, not `.type`, and the resulting template has no `seccompProfile` key (unit); on kind, a real dry-run accepts that patch — the half no fake client can answer (script)                                          | Unit + script |
| AC-U04 | **Pod Security:** in `enforce: restricted` the four always-on fields are skipped and reported while requests are still removed; in `enforce: baseline` all of them are removed                                                                                                                 | Script        |
| AC-U05 | **LimitRange:** a `min` with no default blocks removal of that request and reports it; a LimitRange supplying a default has no record to undo in the first place                                                                                                                               | Script        |
| AC-U06 | Partial undo rewrites the annotation to exactly the surviving records, in the same request; a fully undone target has the annotation removed                                                                                                                                                   | Unit          |
| AC-U07 | A `paused`, `OnDelete` or `partition > 0` target is refused with its remedy named; a protected namespace rejects the object; a `skip`-annotated target **is** undone                                                                                                                           | Unit          |
| AC-U08 | A Deployment's ReplicaSet carries the copied `filled` annotation and is never targeted                                                                                                                                                                                                         | Script        |
| AC-U09 | Preview writes nothing, publishes a hash per target, and a target whose recorded field a human edits between preview and apply is `Stale`                                                                                                                                                      | Unit          |
| AC-U10 | A workload deleted and recreated after hardening carries no annotation and yields no target                                                                                                                                                                                                    | Unit          |
| AC-U11 | On kind: harden a Deployment, approve, apply, undo, approve, apply — the rollout completes twice, the pods stay Ready, and QoS returns to `BestEffort`                                                                                                                                         | Script        |
| AC-U12 | The CRD installs and the API server rejects an empty namespace list, a `matchExpressions` selector, and an edit to any field but `approvedPlan` — `workloadSelector` included                                                                                                                  | Script        |
| AC-U13 | **No silent re-harden:** after a full undo, an `Applied` WorkloadHardening whose generation has not moved issues no **writes** — it reads its own object, as FR-05 always has; editing its `approvedPlan` republishes the undone target with a new hash as `Unapproved`, and does not patch it | Unit          |
| AC-U14 | **The hold:** the revert patch carries `skip: "true"` and `skip-by: <uid>` in the same request, and a later WorkloadHardening reports that target excluded rather than planning it — with no change to 002                                                                                     | Unit          |
| AC-U15 | **The release:** deleting the undo removes both annotations and the finalizer; a `skip` with no `skip-by` keeps both, so a human's hand-set exemption survives an undo's whole lifecycle                                                                                                       | Unit          |
| AC-U16 | **No loop:** a hardening and an undo both naming a namespace converge — the reverted target is excluded from the next hardening plan and neither object churns its hashes across resyncs                                                                                                       | Unit          |
| AC-U17 | A partially reverted target, where BR-U04 gated the securityContext half, is still held                                                                                                                                                                                                        | Unit          |
| AC-U18 | **Exclusivity:** an undo whose selected set includes a workload already carrying another live object's `skip-by` is `Rejected`, naming both; it reaches `Previewed` on the resync after that object is deleted; a `skip-by` naming no live object does not block it                            | Unit          |
| AC-U19 | **Selector:** `workloadSelector` matches the workload's own labels and not the pod template's; two undos over one namespace with disjoint selectors both reach `Applied`; an absent selector covers every workload in the namespace                                                            | Unit          |
| AC-U20 | A workload carrying a human's bare `skip` is reverted but never marked, and the release leaves it alone                                                                                                                                                                                        | Unit          |
| AC-U21 | **The bypass is wider than the revert:** a selected workload with no `filled` record is still held, by a metadata-only patch that changes no pod-template hash and starts no rollout; an **unarmed** undo holds its whole selected set while reverting nothing                                 | Unit          |
| AC-U22 | A `skip` stripped by hand from a held workload is re-asserted on the next resync, and an object whose holds are all intact issues no writes on resync                                                                                                                                          | Unit          |

**AC-U03 and AC-U04 are the two most easily got wrong**, and they fail in opposite ways. AC-U03
fails at the dry-run, loudly, on every target — an undo that never works. AC-U04 passes the
dry-run cleanly and halts the rollout afterwards, which is 002's AC-05 trap repeated at
namespace scope.

Which is why AC-U03 is not a unit test alone. The failure it exists to catch is the API server
refusing `seccompProfile: {}`, and a fake client has no opinion about that: it would accept the
wrong patch and the criterion would pass green all the way to a cluster. The path assertion is
worth keeping as a unit test, because it localises the bug; the proof is the script.

## Error cases

| Case                                                         | Behaviour                                                                                                                                                                                  |
| ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| No workload in the selected set carries the annotation       | `Applied` immediately, with a message saying so; not an error. Nothing was reverted, but the selected set is still held and is released on delete (BR-U09)                                 |
| A held workload's `skip` is stripped by hand                 | Re-asserted on the next resync (BR-U09), at the read cost that rule prices. An object whose holds are all intact still issues no writes                                                    |
| A record's live value was edited                             | Skipped, reported `EditedSinceHardening`, left in the rewritten annotation                                                                                                                 |
| The namespace enforces `restricted`                          | The four PSS fields skipped and reported; requests still removed; both recorded in the rewritten annotation                                                                                |
| A LimitRange `min` with no default covers a recorded request | That request skipped, reported `BlockedByLimitRange`                                                                                                                                       |
| The annotation is malformed or unparseable                   | That target is a finding naming the object; other targets are unaffected. Never guessed at                                                                                                 |
| A recorded path is already absent from the template          | Nothing to delete; the record is dropped from the rewritten annotation, reported `NoRecord`                                                                                                |
| The dry-run refuses the deletion patch                       | That target's outcome records the API server's message; other targets unaffected                                                                                                           |
| A dry-run warning names a Pod Security violation             | Reported as a finding on that target alongside whatever the `enforce` check decided                                                                                                        |
| The undo's own rollout halts                                 | Not detected, as in 002's G-02. `Applied` means the API server accepted the patch                                                                                                          |
| The workload was patched by two `WorkloadHardening` objects  | Only the second object's records exist to undo. G-U01                                                                                                                                      |
| A hardening object's `approvedPlan` is edited after an undo  | The reverted target is excluded by BR-04's skip annotation, so it is absent from the recomputed plan and reported as excluded (BR-U09)                                                     |
| The `skip` annotation was set by a human before the undo ran | `skip-by` is absent, so the release leaves both the annotation and the exemption alone (BR-U10). The undo still reverts the fields it recorded                                             |
| A selected workload is already claimed by another live undo  | `Rejected`, naming the workload and the holding object (BR-U11). Non-terminal, so narrowing the selector or deleting the other rule clears it                                              |
| A `skip-by` names a UID with no live object                  | Treated as unowned and claimed by the next undo that selects the workload (BR-U10)                                                                                                         |
| `workloadSelector` matches nothing in a named namespace      | Not an error. An empty selected set holds nothing and reverts nothing; the object is `Applied`                                                                                             |
| A target is deleted while an undo holding it still exists    | Nothing to release; the finalizer clears on the next pass and does not block the delete                                                                                                    |
| The controller is down when an undo is deleted               | The delete blocks on the finalizer until the controller returns — as a `NetworkIsolation` delete already does, and for strictly less work: this finalizer removes two annotations (BR-U10) |

## Out of scope

### Declined

| #     | Excluded                                                    | Why                                                                                                                                                                                                                                                                                            |
| ----- | ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D-U01 | Restoring a previous value                                  | BR-01 means there was never one. An undo is a deletion, and anything else would be a snapshot feature.                                                                                                                                                                                         |
| D-U02 | Undoing through the original `WorkloadHardening` object     | BR-U01. The record outlives the object on purpose; making the undo need it throws that away.                                                                                                                                                                                                   |
| D-U03 | Modelling admission beyond namespace labels and LimitRanges | Cluster-wide Pod Security defaults and third-party policy engines are unbounded. The dry-run warning (FR-U03) catches what it catches; the rest halts a rollout, which is the pre-existing failure mode.                                                                                       |
| D-U04 | Forcing a removal past a Pod Security refusal               | The operator's remedy is to relabel the namespace or edit the workload, both of which are decisions this tool should not make on their behalf.                                                                                                                                                 |
| D-U05 | Undoing a hardening this tool did not perform               | No annotation, no record, no evidence of what was there before.                                                                                                                                                                                                                                |
| D-U06 | `matchExpressions`, and selecting by namespace label        | 001 serves neither, for the same reason: `matchLabels` covers the cases the brief describes and keeps the blast radius readable in the object. BR-U11's check reads annotations rather than comparing selectors, so a richer selector would not have made it harder — it is simply not needed. |

### Deferred

| #     | Not yet done                                  | Interim position                                                                                                                                                                                    |
| ----- | --------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| G-U01 | A workload patched by two objects in sequence | The second annotation write replaces the first, so object 1's record is lost — 002's G-03. 001 refuses this per namespace (its BR-03); 002 does not. Undo inherits the gap rather than creating it. |
| G-U02 | Watching the undo's rollout to completion     | Shared with 002's G-02, and sharper here: an undo is usually run _because_ a rollout halted.                                                                                                        |
| G-U03 | Undoing a subset of fields rather than all    | The unit of approval is a target. Per-field approval would need a hash per field and a bigger `approvedPlan` than 128 entries allows.                                                               |
| G-U04 | FR-U03's dry-run warning capture              | **Specified, and the written plan carries no task for it.** FR-U03 requires warning headers from the dry-run response to be reported as findings on their target, and the Error cases table has a row for it; `specs/003-bonus/undo/tasks.md` implements it in no task and names it in no coverage table, which is how a requirement described only in an appendix gets lost. It is the cheapest half of BR-U04's signal: exact, version-correct, and free where a namespace carries `warn: restricted`. The `enforce` label and LimitRange checks that FR-U03 calls "not optional" carry the gate without it, so it is a second, corroborating signal rather than the only one. Recorded here rather than in the plan so it survives the plan: whoever executes the plan adds a task for it. |

## Metrics endpoint

About six counters are worth having from a controller that runs once per request — reconciles by
phase, targets patched, targets reverted, dry-run refusals, apply failures, queue depth — and
publishing them closes 001's G-07 and 002's G-07.

**NFR-01 is waived here, deliberately, and the endpoint is built on `prometheus/client_golang`.**
The argument NFR-01 encodes is real and this is the case where it loses: six counters in the text
exposition format are `net/http`, `sync/atomic` and a `fmt.Fprintf` loop in roughly forty lines
with no `go.mod` change, but a hand-rolled exposition is forty lines every reviewer has to read
before trusting, and escaping and `# TYPE` ordering are exactly the details a hand-rolled one gets
subtly wrong. The library is what every scraper and dashboard already assumes.

The cost is named rather than waved through: `go.sum` carries no prometheus today and the repo
vendors, so this is `go get` plus `go mod vendor`, and the dependency brings a transitive set of
its own — a visibly larger `vendor/` for six counters. That is the trade being accepted.

The controller Deployment gains a metrics container port and a Service in front of it. Neither
needs a new RBAC rule: serving metrics reads nothing from the API.

### Grafana

**The raw `prometheus` and `grafana` charts, not `kube-prometheus-stack`.** The stack installs an
operator, its CRDs, node-exporter, kube-state-metrics and a default alert set in order to scrape
six counters — more moving parts than the thing being observed, and slower to stand up than the
cluster the rest of the verification runs on. The two charts on their own are a Prometheus and a
Grafana, which is the whole requirement.

The consequence to plan for is scraping. Without the prometheus-operator's CRDs there is no
`ServiceMonitor`, so the controller is scraped through Prometheus' own `scrape_configs`: either a
`kubernetes_sd_configs` job selecting the pod by namespace and label, or the `prometheus.io/scrape`
annotations the chart's default config already honours. The annotations are the smaller of the
two and are the version to write.

Both charts are installed from a `hack/` script alongside the verification scripts, not from
checked-in dashboards. A dashboard JSON that drifts from the counters is worse than no dashboard,
and the script is what makes the result reproducible by someone who did not write it.

## Integration/e2e tests on kind

Most of this exists. `hack/verify-isolation.sh` already runs the full NetworkIsolation cycle on
a live cluster — TCP and UDP, over pod IP _and_ ClusterIP, before isolation, after enforcement
converges and after the object is deleted, plus a kubelet-readiness check that catches a CNI
enforcing ingress for non-pod sources. `hack/verify-hardening.sh` and
`hack/verify-crd-hardening.sh` cover AC-15 and AC-16, including the limits-only Deployment, the
`default`-only LimitRange, the skip annotation and the provenance annotation.

The actual gap is 002's G-06, and it is narrower than "write integration tests":

1. **`.github/workflows/` exists and is empty — there is no CI at all.** Wire `make test`,
   `make verify`, `make verify-crd`, `make verify-hardening` and `make verify-crd-hardening`
   into one workflow on a kind cluster. This is the whole of the bonus for 001 and 002.
2. **`hack/verify-undo.sh`**, covering AC-U04, AC-U05, AC-U08 and AC-U11 — the criteria a fake
   client cannot reach, because it runs neither admission plugin nor the Deployment controller.
   AC-U12 is covered by envtest below rather than by a third CRD script.
3. **envtest** for the CEL transition rules and the structural schemas of all three CRDs, which
   the scripts cover only as far as `kubectl apply` reports.

   **NFR-01 is waived a second time, for `sigs.k8s.io/controller-runtime`.** The schemas and the
   transition rules are the part of this project with no Go test at all, and they are what an
   operator's `kubectl apply` meets first. The failure that argues loudest for this is specific:
   a CEL transition rule that *errors* rather than refuses — which is what an unguarded copy of
   002's rule does against an optional `workloadSelector` — rejects every update including the
   one that arms the object, and reads as a broken CRD rather than a wrong rule. Nothing catches
   that until something applies the right object in the right order, and a Go test that runs on
   every pull request is a better place for it than a cluster job.

   It **subsumes** the `hack/verify-crd-undo.sh` named above rather than joining it: one
   mechanism per question. `hack/verify-crd-isolation.sh` and `hack/verify-crd-hardening.sh` stay
   as they are — written, passing, and asserting the installed-and-served path rather than the
   schema — but no third script is added, and the cases envtest covers for the first two are
   ported from them rather than invented.

   The control-plane binaries are fetched by `setup-envtest`, which is a build tool run through
   `go run` at a pinned version and is **not** vendored. The risk that comes with it is named
   here because it is this item's version of a green script that tested nothing: a suite that
   skips when the binaries are absent is indistinguishable from one that passed, so CI asserts
   the suite actually ran.

Rewriting the existing scripts from a fresh test plan would rebuild working code.

## Documentation

Diátaxis splits documentation by what the reader is doing: **tutorial** (learning by doing),
**how-to** (achieving a goal), **reference** (looking something up), **explanation**
(understanding why). Its central claim is that mixing two of them in one document serves
neither, and this project is a clean demonstration — it has one mode, has it unusually well, and
has nothing of the other three.

**Explanation already exists, and is the best-written thing in the repository.** It is
`specs/`. Why leaf paths rather than the shallowest path created, why no finalizer on
`WorkloadHardening`, why a hash per target rather than per plan, why requests and never limits —
all argued, with the alternative named and refused. Nothing here rewrites any of it. A second
copy of that reasoning would drift from the one that is load-bearing, and the specs are where an
implementer already looks. The documentation links to them and stops.

**The README is currently trying to be all four at once**, which is the mechanism behind the
duplication the Refactor section describes: setup, decisions, limitations and a time log,
appended once per core task, because there was no other shape available to put them in. Merging
per topic fixes the symptom. This fixes the cause, and changes what that merge should produce: a
**router** — what this is, one path in, and four links — rather than a longer single document.
The two items are done in one pass or the README is rewritten twice.

Three files, not a directory per mode:

| Mode            | File                | Contents                                                                                                                                                                                                 |
| --------------- | ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Tutorial**    | `docs/tutorial.md`  | One path that works: kind up, deploy, isolate two namespaces, harden a workload, approve it, undo it, delete the undo. No choices, no alternatives, no "if you prefer". Every command is a `make` target |
| **How-to**      | `docs/how-to.md`    | One section per goal an operator actually has. Sections, not files — each is a single task, so the document does not mix modes                                                                           |
| **Reference**   | `docs/reference.md` | The annotations, the phases and per-target outcomes, the flags, the RBAC verbs and the metric names                                                                                                      |
| **Explanation** | —                   | `specs/`. Linked, never restated                                                                                                                                                                         |

The how-to sections are the answers that exist today only inside business-rule prose, where an
operator halfway through an incident will not find them: exempt a workload from hardening;
approve a subset of a plan; revert a run that broke something; hand a bypass back; widen the
blast radius deliberately; read a `Stale` row. Each is three to six commands and the one sentence
that says when it is the wrong move.

Reference is half-written already, in the CRD field descriptions, which `kubectl explain` prints
at the terminal where the question is asked. `docs/reference.md` does **not** duplicate them —
it says `kubectl explain` and covers what no CRD schema can hold: the four annotation keys and
who may write each, the phase and outcome vocabulary shared across three kinds, the controller
flags, the RBAC the tool needs and what it deliberately never asks for, and the metric names
once they exist.

**Drift is the only real risk, and it is unequal.** A stale tutorial wastes an afternoon; a
stale reference is believed. The tutorial is cheap to protect: every command in it is a `make`
target, so one loop in CI asserting each named target exists turns a rename into a failed build
rather than a confused reader. Reference gets the same treatment one level down: the
annotation keys, phase names and outcome names are Go constants, so CI greps each string in
`docs/reference.md` and fails if it is not also in `pkg/apis/v1alpha1`. That catches the class of
error that matters — a documented key that no longer exists — and costs one loop in a script.

Out of scope, and stated rather than implied: no documentation site, no generator, no versioned
docs, and no man pages. Four Markdown files in a repository this size, read on the forge that
hosts them, is the whole requirement.
