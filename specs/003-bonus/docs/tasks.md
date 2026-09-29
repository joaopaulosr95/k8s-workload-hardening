# Documentation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the project the three Diátaxis modes it does not have, link to the one it already has, and turn the README into a router.

**Architecture:** Diátaxis splits documentation by what the reader is doing — tutorial, how-to, reference, explanation — and holds that mixing two in one document serves neither. **Explanation already exists and is the best-written thing in the repository: it is `specs/`.** Nothing here rewrites any of it; a second copy of "why leaf paths" would drift from the one that is load-bearing. So: three new files, one per missing mode, and a README that routes to all four. Reference is written first because it is the mode where being stale is worst and the only one with a mechanical correctness check available.

**Tech Stack:** Markdown · bash and GitHub Actions for the two drift checks · GNU make · kind for running the tutorial end to end.

**Spec:** `specs/003-bonus/spec.md`, the **Documentation** section, and the **Docs** paragraph of the Refactor section, whose README merge lands here rather than there.

**Depends on:** every other plan, because it documents them. Run it last. The reference's Metrics section is written only if the metrics plan has landed; the tutorial's undo steps only if the undo plan has.

---

## Global Constraints

- **Four files total, no directories per mode, no documentation site, no generator, no versioned docs, no man pages.** The spec says so: "Four Markdown files in a repository this size, read on the forge that hosts them, is the whole requirement."
- **No mode mixes with another.** A how-to that starts explaining has become an explanation with commands in it, and that is the failure Diátaxis names. Each task carries a check for its own mode.
- **Explanation is never restated.** `specs/` is linked and quoted at most in passing. Every document links into it once for the reader who wants reasoning.
- **The CRD field descriptions are not duplicated.** `kubectl explain` already prints them at the terminal where the question is asked, and a second copy would drift from the schema being served.
- **Both drift checks must be able to fail**, and each task proves it against a synthetic input before trusting it.
- **Nothing in the old README is deleted without being found somewhere else first.** Task 3, Step 7.
- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1, exactly as `go.mod` declares.
- **The repository vendors.** `vendor/` is committed and `go build`/`go test` use it. Never run `go mod tidy`.
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (`AGENTS.md`), enforced by `make cover`. Today's total is 93.5% and the lowest package is 91.7%. `cmd/` is wiring and is excluded.
- **`AGENTS.md` role constraint:** do not edit `specs/003-bonus/spec.md`, and do not create or modify anything else under `specs/` except this file. If the implementation needs behaviour the spec does not describe, stop and raise it rather than inventing a requirement. Every such point this plan already found is listed in "Deviations and clarifications to confirm before merging" at the end.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `refactor:`, `docs:`, `build:`, `ci:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Three conditions, ordered by how much damage each does.

1. **A stale reference is believed; a stale tutorial merely wastes an afternoon.** A documented annotation key that no longer exists gives a reader no way to notice they are wrong. Expected: CI greps every `hardening.acme.corp/*` key and every phase and outcome name out of `docs/reference.md` and fails if it is not also a constant in `pkg/apis/v1alpha1`. → **Task 1, Steps 1 and 2.**
2. **A how-to that starts explaining.** It is the easiest of the four modes to write badly, because the reasoning is interesting and the commands are not. The result is an explanation with commands buried in it, which serves neither reader. Expected: no section opens with "Because", "The reason", or "This is because", and every section carries exactly one link into `specs/` instead. → **Task 2, Steps 3 and 4.**
3. **A README that grows back.** The duplication the Refactor section describes happened because the README was the only shape available for setup, decisions, limitations and a time log. If Decisions and Limitations are merged into the router rather than moved out of it, the same pressure applies to the next feature. Expected: under 60 lines and four or fewer `##` sections, asserted. → **Task 3, Step 6.**

A fourth, handled rather than listed: a documented `make` target that no longer exists. CI fails on it (**Task 3, Step 3**), and every command in the tutorial is a `make` target precisely so that check reaches them.

---

## File Structure

| File | Mode | Responsibility |
| --- | --- | --- |
| `docs/reference.md` | Reference | The annotations and who may remove each, the phase and outcome vocabulary, the flags, the metric names, the RBAC and what it deliberately never asks for. Not the CRD fields — `kubectl explain` serves those. |
| `docs/how-to.md` | How-to | Six goals an operator actually has, each one sentence of when, the commands in full, and one sentence of when not. Sections, not files. |
| `docs/tutorial.md` | Tutorial | One path: cluster, isolation, hardening, approval, undo, release, teardown. No choices in it. |
| `specs/` | Explanation | Already written. Linked, never restated. |
| `README.md` | — | **Rewritten** as a router: what this is, four links, requirements, one Time spent table, the feedback section. |
| `.github/workflows/ci.yml` | — | **Modified.** Two drift checks in the `unit` job: documented identifiers exist, documented `make` targets exist. |

---

### Task 1: `docs/reference.md` and the drift check (Documentation — reference)

**Files:**
- Create: `docs/reference.md`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the CI workflow from the integration-tests plan's Task 1.
- Produces: `docs/reference.md`, and a `docs` job step the other two tasks extend. Task 3's router links to it.

Reference first, because it is the mode where being stale is worst: a stale tutorial wastes an afternoon, a stale reference is believed. It is also the only one with a mechanical correctness check available, so writing it first means the check exists before the prose that needs it.

It does **not** restate the CRD field descriptions. Those are already reference, `kubectl explain` already prints them at the terminal where the question is asked, and a second copy would drift from the schema that is actually served.

- [ ] **Step 1: Write the drift check first**

Add to `.github/workflows/ci.yml`, in the `unit` job:

```yaml
      - name: Reference documentation names things that exist
        run: |
          set -euo pipefail
          fail=0
          # Every hardening.acme.corp/* key and every Phase/Outcome value the
          # reference names must also be a constant in the API package. A
          # documented key that no longer exists is the failure mode that
          # matters here, because a reader has no way to notice.
          for s in $(grep -oE 'hardening\.acme\.corp/[a-z-]+' docs/reference.md | sort -u); do
            grep -qrF "\"$s\"" pkg/apis/v1alpha1/ || { echo "reference names $s, which is not a constant in pkg/apis/v1alpha1"; fail=1; }
          done
          for s in $(grep -oE '^\| `[A-Z][A-Za-z]+` ' docs/reference.md | tr -d '|` '); do
            grep -qrE "Phase = \"$s\"|Outcome[A-Za-z]* = \"$s\"" pkg/apis/v1alpha1/ || { echo "reference names phase/outcome $s, which is not a constant in pkg/apis/v1alpha1"; fail=1; }
          done
          [ "$fail" -eq 0 ] && echo "ok: every documented identifier exists"
          exit "$fail"
```

- [ ] **Step 2: Run it against an empty file to prove it can fail**

```bash
printf '| `NotAPhase` |\nhardening.acme.corp/invented\n' > /tmp/ref-probe.md
grep -oE 'hardening\.acme\.corp/[a-z-]+' /tmp/ref-probe.md | while read -r s; do
  grep -qrF "\"$s\"" pkg/apis/v1alpha1/ || echo "would fail on: $s"
done
```

Expected: `would fail on: hardening.acme.corp/invented`. A check that cannot fail is worse than no check, because it is believed — the same reasoning as the coverage floor in the refactor plan's Task 4.

- [ ] **Step 3: Write the reference**

Create `docs/reference.md` with exactly these sections and nothing else. No prose about *why* — that is `specs/`, linked once at the top.

````markdown
# Reference

Look-up only. For why any of this is the way it is, read the specs:
[001](../specs/001-network-isolation/spec.md), [002](../specs/002-workload-hardening/spec.md),
[003](../specs/003-bonus/spec.md). For the fields of any custom resource, `kubectl explain`
prints the schema this repository ships — it is not duplicated here.

```console
$ kubectl explain workloadhardening.spec
$ kubectl explain workloadhardeningundo.spec.workloadSelector
$ kubectl explain networkisolation.spec.peers
```

## Custom resources

| Kind | Plural | Short | Scope |
| --- | --- | --- | --- |
| `NetworkIsolation` | `networkisolations` | `ni` | Namespaced |
| `WorkloadHardening` | `workloadhardenings` | `wh` | Namespaced |
| `WorkloadHardeningUndo` | `workloadhardeningundos` | `whu` | Namespaced |

All three are `hardening.acme.corp/v1alpha1`, all three have a status subresource.

## Annotations and labels

| Key | Written on | Written by | Removed by |
| --- | --- | --- | --- |
| `hardening.acme.corp/filled` | the patched workload | `WorkloadHardening` | `WorkloadHardeningUndo`, when every record it holds is reverted |
| `hardening.acme.corp/skip` | a workload | a human, or `WorkloadHardeningUndo` | whoever wrote it, and nobody else |
| `hardening.acme.corp/skip-by` | a workload | `WorkloadHardeningUndo` | the object whose UID it names |
| `hardening.acme.corp/owner` | a generated NetworkPolicy | `NetworkIsolation` | its finalizer |
| `hardening.acme.corp/operation` (label) | a generated NetworkPolicy | `NetworkIsolation` | its finalizer |

A `skip` with no `skip-by` is a human's exemption. Nothing in this tool ever removes it.

## Finalizers

| Finalizer | On | Removes |
| --- | --- | --- |
| `hardening.acme.corp/cleanup` | `NetworkIsolation` | the NetworkPolicies it generated |
| `hardening.acme.corp/undo-release` | `WorkloadHardeningUndo` | `skip` and `skip-by` from the workloads it holds |

`WorkloadHardening` carries no finalizer: deleting it leaves the patches and the provenance
annotation in place.

## Phases

| Phase | Kinds | Terminal |
| --- | --- | --- |
| `Pending` | all three | no |
| `Rejected` | all three | no — re-evaluated every resync |
| `Active` | `NetworkIsolation` | no |
| `Degraded` | `NetworkIsolation` | no |
| `Deleting` | `NetworkIsolation` | no |
| `Previewed` | `WorkloadHardening`, `WorkloadHardeningUndo` | no |
| `Applied` | `WorkloadHardening`, `WorkloadHardeningUndo` | yes, until `spec.approvedPlan` changes — and for an undo, only for its reverts; its holds are maintained for the object's whole life |
| `PartiallyApplied` | `WorkloadHardening`, `WorkloadHardeningUndo` | no |

## Per-target outcomes

| Outcome | Meaning |
| --- | --- |
| `Planned` | would change, on an object nobody has armed |
| `Patched` | fields written |
| `Reverted` | fields deleted |
| `Held` | selected and held out of hardening, with nothing to revert |
| `Failed` | the API server refused it |
| `Stale` | the approved hash no longer describes this target |
| `Unapproved` | not in `spec.approvedPlan` |

## Controller flags

| Flag | Default | Effect |
| --- | --- | --- |
| `-kubeconfig` | "" (in-cluster) | path to a kubeconfig |
| `-resync` | `30s` | how often every object re-validates its preconditions |
| `-timeout` | `30s` | deadline for one reconcile pass's API calls |
| `-protected-namespaces` | "" | comma-separated, in addition to the built-ins |
| `-metrics-addr` | `:8080` | metrics listener; empty disables it |

Built-in protected namespaces: `kube-system`, `kube-public`, `kube-node-lease`, and the
controller's own namespace.

## Metrics

Served on `-metrics-addr` at `/metrics`.

| Series | Type | Labels |
| --- | --- | --- |
| `hardening_reconcile_total` | counter | `resource`, `phase` |
| `hardening_targets_patched_total` | counter | — |
| `hardening_targets_reverted_total` | counter | — |
| `hardening_dryrun_refusals_total` | counter | — |
| `hardening_apply_failures_total` | counter | — |
| `hardening_queue_depth` | gauge | — |

## RBAC

Cluster-scoped, because target namespaces are chosen at runtime.

| Resource | Verbs |
| --- | --- |
| `networkpolicies` | create, get, list, watch, update, delete |
| `deployments`, `statefulsets`, `daemonsets` | get, list, patch |
| `replicasets`, `jobs`, `cronjobs` | get, list |
| `pods`, `namespaces` | get, list, watch |
| `limitranges` | get, list |
| the three custom resources, their `status` and `finalizers` | get, list, watch, update, patch |

Deliberately never requested: `delete` on any workload, `create` on any workload,
`resourcequotas`, pod `exec`, and `secrets`.
````

Fill the Metrics section only if the metrics plan has landed; if it has not, delete that section rather than documenting something that does not answer. The drift check does not cover metric names — they live in `pkg/metrics`, not `pkg/apis/v1alpha1` — so the metrics plan's own CI assertion is what protects them.

- [ ] **Step 4: Run the drift check against the real file**

```bash
for s in $(grep -oE 'hardening\.acme\.corp/[a-z-]+' docs/reference.md | sort -u); do
  grep -qrF "\"$s\"" pkg/apis/v1alpha1/ || echo "MISSING: $s"
done
```

Expected: **no output**. If `hardening.acme.corp/operation` or `hardening.acme.corp/owner` fails here, the grep is matching the label constant's value rather than its name — check `pkg/apis/v1alpha1/isolation.go` and fix the check, not the document.

- [ ] **Step 5: Verify nothing was duplicated from the CRDs**

```bash
for f in deploy/crd*.yaml; do
  python3 - "$f" <<'EOF'
import sys, re
text = open(sys.argv[1]).read()
for d in re.findall(r'description: >-\n((?:\s{4,}.*\n)+)', text):
    line = " ".join(d.split())[:60]
    if line and line in open("docs/reference.md").read():
        print("duplicated from", sys.argv[1], ":", line)
EOF
done
```

Expected: **no output**. A CRD description copied into the reference is the drift this task exists to avoid.

- [ ] **Step 6: Commit**

```bash
git add docs/reference.md .github/workflows/ci.yml
git commit -m "docs(reference): what to look up, and a check that it still exists

The Diátaxis reference quadrant. Look-up only — no reasoning, which is specs/,
linked once at the top and never restated.

It does not duplicate the CRD field descriptions. kubectl explain already
prints them at the terminal where the question is asked, and a second copy
would drift from the schema actually being served. What is here is what no CRD
schema can hold: the annotation keys and who may remove each, the phase and
outcome vocabulary shared across three kinds, the flags, the metric names, and
the RBAC including the five things the tool deliberately never asks for.

CI greps every hardening.acme.corp/* key and every phase and outcome name out
of the document and fails if it is not also a constant in pkg/apis/v1alpha1.
A stale tutorial wastes an afternoon; a stale reference is believed.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `docs/how-to.md` (Documentation — how-to)

**Files:**
- Create: `docs/how-to.md`

**Interfaces:**
- Consumes: `docs/reference.md` from Task 1, linked from each section rather than restated.
- Produces: `docs/how-to.md`. Task 3's router links to it.

Six goals an operator actually has. Each answer exists today, inside business-rule prose in `specs/`, where somebody halfway through an incident will not find it. Sections rather than one file per goal: each section is a single task, so the document does not mix modes, and six files of twelve lines each is a directory to navigate for no gain.

Every section has the same shape and nothing else: one sentence saying when this is the right move, three to six commands, one sentence saying when it is the wrong one. No background, no alternatives, no reasoning — a link to the spec covers all three.

- [ ] **Step 1: Write the document**

Create `docs/how-to.md` with exactly these six sections:

| Section | Ends with — when this is the wrong move |
| --- | --- |
| **Exempt a workload from hardening** — annotate it `hardening.acme.corp/skip: "true"` by hand, with no `skip-by`, which nothing in this tool will ever remove | if the workload only needs *different* values, the policy is per-object; create a second `WorkloadHardening` instead |
| **Approve part of a plan** — read `status.plan`, copy the hashes of the targets you want into `spec.approvedPlan`, leave the rest | if every target is wanted, approving all of them in one edit is one rollout window rather than several |
| **Revert a run that broke something** — create a `WorkloadHardeningUndo` over the namespaces, read its plan, approve, apply | if the rollout has merely halted and the template is right, `kubectl rollout undo` is faster and touches nothing else |
| **Hand a bypass back** — `kubectl delete workloadhardeningundo <name>`, which releases every workload it holds | if only one workload should return to hardening, narrow the undo's selector instead: deleting it releases all of them |
| **Widen the blast radius deliberately** — `spec.namespaces` is immutable, so this is a new object, not an edit | if the new namespaces need different request values, that is a second object regardless |
| **Read a `Stale` row** — compare `status.plan[].approvedHash` with `status.plan[].hash`, then re-approve the new one | if several targets are `Stale` at once, something is editing the workloads; find it before re-approving, or the next approval is stale too |

Each section's commands come from the tutorial's happy path (Task 3) with the branches the tutorial refuses to show. Write the commands out in full — an operator mid-incident does not want to assemble them from a description.

- [ ] **Step 2: Verify every command runs**

```bash
make kind-up || true
make deploy
grep -oE '^\$ .*' docs/how-to.md | sed 's/^\$ //' > /tmp/howto-cmds.txt
wc -l /tmp/howto-cmds.txt
```

Then run each one that is safe to run against the sample namespaces and confirm it does not error on a flag or a field path. `kubectl explain`-style typos and a renamed `-o jsonpath` are the two failures this catches, and both read as "the tool is broken" to somebody following the page.

- [ ] **Step 3: Verify no section explains anything**

```bash
grep -nE '^(Because|The reason|This is because|Note that the rationale)' docs/how-to.md
```

Expected: **no output**. A how-to that starts explaining has become an explanation with commands in it, which is the failure mode Diátaxis names, and the explanation already exists in `specs/`.

- [ ] **Step 4: Verify every section links to its rule**

```bash
awk '/^## /{s=$0; seen=0} /specs\//{seen=1} /^## /&&NR>1{if(!seen) print "no spec link: " prev} {prev=s}' docs/how-to.md
```

Expected: **no output**. Each section carries one link into `specs/` for the reader who wants the reasoning, which is how this document stays short.

- [ ] **Step 5: Commit**

```bash
git add docs/how-to.md
git commit -m "docs(how-to): six goals, each with its commands and its counter-case

The Diátaxis how-to quadrant. Every one of these answers already existed,
inside business-rule prose in specs/, where an operator halfway through an
incident will not find it.

Sections rather than a file per goal: each section is one task, so the
document does not mix modes, and six twelve-line files is a directory to
navigate for no gain.

Each section is one sentence on when this is right, the commands in full, and
one sentence on when it is wrong — the last of which is the part a
reasoning-free how-to usually drops, and the part that stops somebody deleting
an undo when they meant to narrow its selector.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `docs/tutorial.md` and the README router (Documentation — tutorial)

**Files:**
- Create: `docs/tutorial.md`
- Modify: `README.md`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `docs/reference.md` (Task 1), `docs/how-to.md` (Task 2), and the make targets the refactor plan's Task 4 renamed.
- Produces: nothing other tasks read. This is the last task in this plan and the last in the bonus.

The README merge the spec's Refactor section asks for happens **here**, not in the refactor plan, and produces a router rather than a longer document — the spec's Documentation section says so, and doing it in the refactor plan would mean writing the README twice.

- [ ] **Step 1: Write the tutorial**

Create `docs/tutorial.md`: one path, from nothing to a reverted workload. No choices, no alternatives, no "if you prefer" — every one of those belongs in `docs/how-to.md`.

The path, each step a `make` target or a single `kubectl`:

1. `make kind-up` — a cluster
2. `make deploy` — CRDs, RBAC, controller
3. `make samples-isolation` — two namespaces that can talk to each other; prove it with the curl the sample runs
4. Apply a `NetworkIsolation`; watch `Active`; prove they can no longer talk
5. `make samples-hardening` — a Deployment with no requests and a root container
6. Apply `deploy/samples/hardening.yaml`; watch `Previewed`; **read `status.plan`** — this is the step the whole design exists for, so the tutorial stops and reads it
7. Copy the hashes into `spec.approvedPlan`; watch `Applied`; see the fields on the template and the `filled` annotation beside them
8. Apply `deploy/samples/undo.yaml`; watch it hold the workloads **before** anything is approved
9. Copy its hashes in; watch `Applied`; see the fields gone, `filled` gone, `skip` and `skip-by` in their place
10. `kubectl delete workloadhardeningundo tenant-rollback` — the workloads are eligible for hardening again
11. `make kind-down`

Each step: the command, what to expect, and one line on what just happened. Where a step waits, use the `kubectl wait --for=jsonpath` the verification scripts already use rather than telling the reader to watch.

- [ ] **Step 2: Run it start to finish, from a cold machine**

```bash
make kind-down || true
# then every command in docs/tutorial.md, in order, pasted by hand
```

Expected: every step produces what the document says it produces. Paste them rather than scripting them — the point is that a reader typing these gets this result, and a script would paper over a missing `wait`.

- [ ] **Step 3: Add the make-target drift check**

Add to `.github/workflows/ci.yml`, in the `unit` job:

```yaml
      - name: Documentation names make targets that exist
        run: |
          set -euo pipefail
          fail=0
          for t in $(grep -rhoE '\bmake [a-z][a-z-]*' README.md docs/*.md | cut -d' ' -f2 | sort -u); do
            make -n "$t" >/dev/null 2>&1 || { echo "documentation names 'make $t', which does not exist"; fail=1; }
          done
          [ "$fail" -eq 0 ] && echo "ok: every documented make target exists"
          exit "$fail"
```

`make -n` needs no cluster: it prints the recipe without running it.

- [ ] **Step 4: Prove that check can fail**

```bash
echo 'run make invented-target to begin' > /tmp/doc-probe.md
make -n invented-target >/dev/null 2>&1 || echo "would fail on: invented-target"
```

Expected: `would fail on: invented-target`.

- [ ] **Step 5: Rewrite the README as a router**

Replace `README.md` entirely. The duplication the Refactor section describes — `# core task 2` inside `## Setup`, and 135 lines mirroring Decisions, Limitations, What I'd do and Time spent once per core task — goes away because those sections move to where they belong, not because they are merged:

```markdown
# k8s-workload-hardening

Two Kubernetes controllers in one binary: `NetworkIsolation` writes the pair of NetworkPolicies
that isolate two groups of pods from each other, and `WorkloadHardening` fills in missing
resource requests and missing securityContext fields across namespaces, previewing every change
and taking approval per workload. `WorkloadHardeningUndo` takes the second one back.

- **New here?** [Tutorial](docs/tutorial.md) — one path, cluster to reverted workload, ~15 minutes.
- **Trying to do something?** [How-to](docs/how-to.md) — six goals, commands first.
- **Looking something up?** [Reference](docs/reference.md) — annotations, phases, flags, RBAC, metrics.
- **Want to know why?** [specs/](specs/) — every decision with the alternative named and refused.

## Requirements

[versions table, moved verbatim from the current ## Tested versions]

## Time spent

[ONE table, both core tasks and the bonus as rows — merged from the two the current README carries]

## Feedback on the brief

[moved verbatim]
```

Decisions, Limitations and What I'd do with more time do **not** move into the router. They are explanation, they are already in `specs/` in more detail and better argued, and keeping a second shorter copy in the README is the exact duplication this whole item exists to end. The router links to `specs/`; if a reviewer needs a summary, the specs' own "Out of scope" tables are it.

- [ ] **Step 6: Verify the router is a router**

```bash
wc -l README.md
grep -c '^## ' README.md
```

Expected: under 60 lines and four or fewer `##` sections. A README that grows past that is becoming a fifth document, which is where this started.

- [ ] **Step 7: Verify nothing was lost**

```bash
git show HEAD~1:README.md > /tmp/readme-old.md
grep -oE '^\| [0-9].*' /tmp/readme-old.md | head
```

Read the old Decisions and Limitations sections one last time and confirm each point exists in `specs/`. Anything that does **not** is a fact about the project that was only ever in the README — move it into the relevant spec's Out of scope or Deferred table and say so in the commit, or keep it in the router. Do not delete it.

- [ ] **Step 8: Run both drift checks and the full suite**

```bash
go test ./... -race && make cover
for t in $(grep -rhoE '\bmake [a-z][a-z-]*' README.md docs/*.md | cut -d' ' -f2 | sort -u); do make -n "$t" >/dev/null 2>&1 || echo "MISSING: make $t"; done
```

Expected: every package `ok`, coverage at or above 90%, and no `MISSING` line.

- [ ] **Step 9: Commit**

```bash
git add docs/tutorial.md README.md .github/workflows/ci.yml
git commit -m "docs(tutorial): one path that works, and a README that routes

The Diátaxis tutorial quadrant: cluster to isolated namespaces to a hardened
workload to a reverted one, with no choices in it. Every alternative belongs
in the how-to, and a tutorial that offers them stops being one.

The README merge the Refactor section asks for happens here rather than there,
and produces a router rather than a longer document — which is what the
Documentation section of the spec says it should, and doing it in the refactor
plan would have meant writing the README twice.

Decisions, Limitations and What I'd do with more time do not survive into it.
They are explanation, they are in specs/ in more detail and better argued, and
a second shorter copy is the duplication this item exists to end. Step 7 checks
each point exists there before it goes.

CI now fails if any make target named in the README or docs/ does not exist, so
a renamed target breaks the build rather than the reader.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Review Focus coverage

| # | Condition | Task, Step |
| --- | --- | --- |
| 1 | A stale reference | 1 Steps 1 and 2 |
| 2 | A how-to that explains | 2 Steps 3 and 4 |
| 3 | A README that grows back | 3 Step 6 |
| — | A documented `make` target that does not exist | 3 Steps 3 and 4 |

## Deviations and clarifications to confirm before merging

1. **The README merge moves here from the Refactor section.** The spec asks for it there and then, in its Documentation section, says the merge should produce a **router** rather than a longer document — because the README trying to be all four modes at once is the mechanism behind the duplication in the first place. Doing it in the refactor plan would mean writing the README twice. The duplication is still removed, by relocation rather than by merging.

2. **Decisions, Limitations and What I'd do with more time do not survive into the router.** They are explanation; they are in `specs/` in more detail and better argued; and a second shorter copy is the exact duplication this item exists to end. Task 3, Step 7 reads the old sections one last time and requires every point to be findable in `specs/` before it goes — anything that is not gets moved into the relevant spec's Out of scope or Deferred table, or kept. Nothing is deleted on the assumption it is elsewhere. **This is the item most worth a second opinion:** a reviewer who expects a self-contained README will find the new one thin, and that is deliberate rather than accidental.

3. **The reference's Metrics section is conditional.** If the metrics plan has not landed, delete that section rather than documenting an endpoint that does not answer. The drift check does not cover metric names — they live in `pkg/metrics`, not `pkg/apis/v1alpha1` — so the metrics plan's own CI assertion is what protects them.

4. **Six how-to sections, not more.** Each is a goal somebody has mid-incident. The temptation is to add one per business rule, which would make the document a second reference and reintroduce the mixing this plan exists to end. If a seventh is genuinely needed, it replaces one rather than joining it.

5. **The tutorial is verified by a human pasting it, not by a script.** Task 3, Step 2 says so explicitly. A script would paper over a missing `kubectl wait` — the reader would hit a race the CI never sees — and the claim the tutorial makes is precisely that somebody typing these commands gets this result.
