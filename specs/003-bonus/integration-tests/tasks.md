# Integration and e2e Tests on kind — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the verification that already exists under CI, and add the two scripts that cover what a fake client cannot reach.

**Architecture:** Most of this exists. `hack/verify-isolation.sh` already runs the full NetworkIsolation cycle on a live cluster, and `hack/verify-hardening.sh` and `hack/verify-crd-hardening.sh` already cover AC-15 and AC-16. The gap is that **nothing runs them**: `.github/workflows/` exists and is empty. Task 1 is the whole of the bonus for 001 and 002. Tasks 2 and 3 extend the existing script mechanism — same helpers, same insistence on an expected error substring — to the third CRD and to the undo's five script-only criteria.

**Tech Stack:** GitHub Actions · kind v0.32.0 · kubectl v1.36.2 · bash · GNU make · Go 1.27.1 for the unit job.

**Spec:** `specs/003-bonus/spec.md`, the **Integration/e2e tests on kind** section.

**Depends on:** the refactor plan's Task 4 for the `verify` and `verify-crd` aggregates, and the undo plan for everything Tasks 2 and 3 verify. Rewriting the existing scripts from a fresh test plan would rebuild working code, and this plan does not.

---

## Global Constraints

- **No dependency changes and no Go code.** This plan writes YAML and bash only.
- **The existing scripts are not rewritten.** `hack/verify-isolation.sh`, `hack/verify-hardening.sh` and `hack/verify-crd-hardening.sh` are finished. The only change any of them may receive is a rename, and that belongs to the refactor plan.
- **Every `expect_reject` asserts a specific error substring.** `hack/verify-crd-hardening.sh` carries the reason in a comment: "without it a manifest that fails to parse, or one rejected for an unrelated reason, reads as a passing test." Task 2 follows it.
- **The CI cluster name must equal the Makefile's `CLUSTER`.** `kind load docker-image` names the cluster explicitly.
- **Failure must be diagnosable from the job log alone.** A red kind job with no controller logs costs a full re-run to understand.
- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1, exactly as `go.mod` declares.
- **The repository vendors.** `vendor/` is committed and `go build`/`go test` use it. Never run `go mod tidy`.
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (`AGENTS.md`), enforced by `make cover`. Today's total is 93.5% and the lowest package is 91.7%. `cmd/` is wiring and is excluded.
- **`AGENTS.md` role constraint:** do not edit `specs/003-bonus/spec.md`, and do not create or modify anything else under `specs/` except this file. If the implementation needs behaviour the spec does not describe, stop and raise it rather than inventing a requirement. Every such point this plan already found is listed in "Deviations and clarifications to confirm before merging" at the end.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `refactor:`, `docs:`, `build:`, `ci:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Three conditions, ordered by how likely each is to waste someone's afternoon.

1. **A CI cluster name that disagrees with the Makefile's.** `kind load docker-image` names the cluster explicitly; a mismatch loads the image into a cluster `kubectl` is not pointing at, and every pod sits in `ErrImageNeverPull` with no error naming the cause. Expected: the two strings are compared in the job that depends on them. → **Task 1, Step 2.**
2. **A green script that tested nothing.** `hack/verify-undo.sh` passes trivially if the namespaces it needs do not produce the admission conditions it is checking — a missing `enforce` label, or a LimitRange that happens to carry a default. Expected: the run asserts that at least one `BlockedByPodSecurity` and one `BlockedByLimitRange` finding actually appeared. → **Task 3, Step 5.**
3. **A transition rule that rejects everything rather than only what it should.** An unguarded copy of 002's CEL rule raises a runtime error on a `WorkloadHardeningUndo` with no `workloadSelector`, and an erroring transition rule rejects the update — including the `approvedPlan` edit that arms the object. That reads as "the CRD is broken", not as "the rule is wrong". Expected: two accepts and two rejects pin it. → **Task 2, Step 4.**

---

## File Structure

| File | Responsibility |
| --- | --- |
| `.github/workflows/ci.yml` | **New.** Two jobs: `go vet` plus the unit suite behind the coverage floor, and a kind cluster running `make verify-crd` and `make verify`. Controller logs dumped on failure. |
| `hack/verify-crd-undo.sh` | **New.** AC-U12 against a real API server: fifteen cases, each asserting a specific error substring. |
| `hack/verify-undo.sh` | **New.** AC-U03's dry-run half, AC-U04, AC-U05, AC-U08 and AC-U11 on a live cluster. |
| `deploy/samples/workloads-undo.yaml` | **New.** Three namespaces producing both of BR-U04's gates plus a plain one, and the four Deployments they need. |
| `Makefile` | **Modified.** `verify-crd-undo`, `samples-undo`, `verify-undo`, and both added to the aggregates. |

---

### Task 1: CI on every push (G-06 item 1)

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `make test`, `make cover`, `make verify`, `make verify-crd` from the refactor plan's Task 4.
- Produces: a workflow named `ci` with jobs `unit` and `kind`. Tasks 2 and 3 add the undo suites to the `verify` and `verify-crd` aggregates this job already runs, so the workflow needs no further edit for them.

`.github/workflows/` exists and is empty — there is no CI in this repository at all, which the spec calls "the whole of the bonus for 001 and 002".

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

# A push to a branch with an open PR would otherwise run this twice, and the
# kind job is the expensive half.
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          # The repository vendors its dependencies, so there is nothing to
          # download and nothing worth caching between runs.
          cache: false
      - name: go vet
        run: go vet ./pkg/... ./cmd/...
      - name: Unit tests
        run: make test
      - name: Coverage floor
        run: make cover

  kind:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: false
      - name: Create the cluster
        uses: helm/kind-action@v1
        with:
          # Must match CLUSTER in the Makefile: `kind load docker-image`
          # names the cluster explicitly, and a mismatch loads the image
          # into a cluster kubectl is not pointing at.
          cluster_name: hardening
          config: hack/kind/cluster.yaml
      - name: CRD schema and CEL rules
        run: make verify-crd
      - name: NetworkIsolation and WorkloadHardening, end to end
        run: make verify
      - name: Controller logs on failure
        if: failure()
        run: kubectl -n isolation-system logs deployment/network-isolation --tail=200 || true
```

- [ ] **Step 2: Verify the cluster name matches the Makefile (Review Focus 3)**

```bash
make_cluster=$(awk -F'?= *' '/^CLUSTER \?=/ {print $2}' Makefile)
ci_cluster=$(awk '/cluster_name:/ {print $2}' .github/workflows/ci.yml)
echo "make=$make_cluster ci=$ci_cluster"
test "$make_cluster" = "$ci_cluster" || echo "MISMATCH — kind load would target the wrong cluster"
```

Expected: `make=hardening ci=hardening` and no MISMATCH line.

- [ ] **Step 3: Verify the workflow parses**

```bash
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('ok')"
```

Expected: `ok`.

- [ ] **Step 4: Verify every make target the workflow names exists**

```bash
for t in test cover verify verify-crd; do
  make -n "$t" >/dev/null 2>&1 || echo "MISSING target: $t"
done
```

Expected: **no output**. `make -n verify` needs no cluster because it only prints.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: run the unit suite and the kind verification on every push

.github/workflows existed and was empty — there was no CI at all, which is
the actual gap behind 002's G-06 rather than 'write integration tests'. The
scripts already cover AC-15 and AC-16 on a live cluster; nothing ran them.

Two jobs: vet plus the unit suite behind the 90% floor, and a kind cluster
running verify-crd and verify. Controller logs are dumped on failure, because
a red kind job with no logs costs a full re-run to diagnose.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 6: Push the branch and confirm the workflow goes green**

```bash
git push -u origin HEAD
gh run watch
```

Expected: both jobs pass. This is the only task in the plan whose deliverable cannot be verified locally, so it is verified here rather than assumed.

---

### Task 2: `hack/verify-crd-undo.sh` (G-06 item 3 in practice; AC-U12)

**Files:**
- Create: `hack/verify-crd-undo.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `deploy/crd-undo.yaml` from the undo plan's Task 4.
- Produces: `make verify-crd-undo`, and `verify-crd` as a three-way aggregate. Task 1's `kind` job already runs `make verify-crd`, so it needs no edit.

The spec's third item asks for envtest here. This task is what replaces it — see the Deviations section. `hack/verify-crd-hardening.sh` already runs fourteen cases against a real API server and asserts a specific error substring for each; this extends that mechanism to the third CRD rather than introducing a second one.

- [ ] **Step 1: Write the script**

Create `hack/verify-crd-undo.sh`, modelled line for line on `hack/verify-crd-hardening.sh` — same `expect_reject` / `expect_accept` helpers, same insistence on an expected error substring, because "without it a manifest that fails to parse, or one rejected for an unrelated reason, reads as a passing test".

Cases (AC-U12):

| Case | Expected substring |
| --- | --- |
| empty namespace list | `should have at least 1 items` |
| seventeen namespaces | `must have at most 16 items` |
| duplicate namespace | `Duplicate value` |
| namespaces omitted | `spec.namespaces: Required value` |
| a `matchExpressions` selector | `matchExpressions` |
| nine `matchLabels` entries | `must have at most 8 properties` |
| an empty `matchLabels` | `should have at least 1 properties` |
| a thirteen-character hash | `approvedPlan[0] in body should match` |
| **accept** a valid object with no selector | — |
| **accept** arming it with `approvedPlan` | — |
| reject an edit to `spec.namespaces` | `only spec.approvedPlan may be changed` |
| **reject adding** a `workloadSelector` to the selectorless object | `only spec.approvedPlan may be changed` |
| **accept** a valid object *with* a selector | — |
| reject an edit to that object's `workloadSelector` | `only spec.approvedPlan may be changed` |
| **reject removing** its `workloadSelector` | `only spec.approvedPlan may be changed` |

The last four are the point: they are what the `has()` guards buy, and an unguarded copy of 002's rule fails the two "accept" cases on a selectorless object with a CEL evaluation error rather than the transition message.

```bash
chmod +x hack/verify-crd-undo.sh
```


- [ ] **Step 2: Make it executable and add the make target**

```bash
chmod +x hack/verify-crd-undo.sh
```

```make
verify-crd-undo:
	./hack/verify-crd-undo.sh

verify-crd: verify-crd-isolation verify-crd-hardening verify-crd-undo
```

Add `verify-crd-undo` to `.PHONY`. `verify-crd` is the aggregate the refactor plan's Task 4 created and Task 1's `kind` job already runs.

- [ ] **Step 3: Run it against a live cluster**

```bash
make kind-up || true
kubectl apply -f deploy/crd-undo.yaml
make verify-crd-undo
```

Expected: every case reports `ok`, and the script prints `AC-U12 PASSED`.

- [ ] **Step 4: Confirm the four transition cases actually fired**

```bash
make verify-crd-undo | grep -c 'only spec.approvedPlan may be changed'
```

Expected: `4`. Those four are what the `has()` guards buy. An unguarded copy of 002's rule fails the two *accept* cases on a selectorless object with a CEL evaluation error instead, so a run where they do not appear has tested the wrong thing.

- [ ] **Step 5: Verify the aggregate reaches it**

```bash
make -n verify-crd | grep -c verify-crd-undo.sh
```

Expected: `1`.

- [ ] **Step 6: Commit**

```bash
git add hack/verify-crd-undo.sh Makefile
git commit -m "test(undo): AC-U12 against a real API server

Fifteen cases, each asserting a specific error substring — without one, a
manifest that fails to parse or is rejected for an unrelated reason reads as a
passing test, which is the comment verify-crd-hardening.sh already carries.

Four of them are the transition rule. workloadSelector is optional, so an
unguarded copy of 002's rule raises a CEL runtime error on an object that omits
it, and an erroring transition rule rejects every update including the
approvedPlan edit that arms the object. Two accepts and two rejects pin that.

Extends the existing mechanism rather than adding envtest beside it: same
assertions, same admission chain, on the cluster CI already starts.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `hack/verify-undo.sh` (G-06 item 2; AC-U03, AC-U04, AC-U05, AC-U08, AC-U11)

**Files:**
- Create: `deploy/samples/workloads-undo.yaml`
- Create: `hack/verify-undo.sh`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: everything.
- Produces: `make verify-undo`, and two CI steps.

AC-U03's dry-run half, AC-U04, AC-U05, AC-U08 and AC-U11 are script-only: a fake client runs neither admission plugin, nor the Deployment controller, nor the CRD's schema, and validates no API type at all.

- [ ] **Step 1: Write the sample workloads**

Create `deploy/samples/workloads-undo.yaml`:

```yaml
# Namespaces and workloads for hack/verify-undo.sh. Three namespaces, because
# the two admission gates BR-U04 names cannot be produced in one.
---
# No enforce label, no LimitRange: everything this tool wrote is removable.
apiVersion: v1
kind: Namespace
metadata: {name: undo-open}
---
# AC-U04: the restricted standard requires the four always-on fields, so
# removing them makes every new pod inadmissible. Requests are still removable.
apiVersion: v1
kind: Namespace
metadata:
  name: undo-restricted
  labels:
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
---
# AC-U04's other half: baseline requires none of the four.
apiVersion: v1
kind: Namespace
metadata:
  name: undo-baseline
  labels: {pod-security.kubernetes.io/enforce: baseline}
---
# AC-U05: a Container min with NO default and NO defaultRequest. A pod with no
# cpu request is rejected, so the request this tool filled cannot be removed.
apiVersion: v1
kind: LimitRange
metadata: {name: floor, namespace: undo-open}
spec:
  limits:
    - type: Container
      min: {cpu: 5m}
---
# AC-U19: two workloads with different own labels, so a selector can pick one.
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: undo-open
  labels: {app: api}
spec:
  replicas: 1
  selector: {matchLabels: {app: api}}
  template:
    metadata: {labels: {app: api}}
    spec:
      containers:
        - {name: app, image: registry.k8s.io/pause:3.10}
# ... plus `worker` in undo-open with labels {app: worker}, and one Deployment
# in each of undo-restricted and undo-baseline. Every pod template starts bare,
# so WorkloadHardening finds a full set of gaps in each.
```

Complete the file with the four Deployments described in that trailing comment; the restricted namespace's Deployment needs `runAsNonRoot`, `runAsUser: 65532`, `seccompProfile`, `allowPrivilegeEscalation: false` and `capabilities.drop: [ALL]` already set at admission time or its pods will not start there — which is the point: hardening fills what is absent, and only requests are absent in that namespace.

- [ ] **Step 2: Write the verification script**

Create `hack/verify-undo.sh`, using the `ok`/`bad`/`check`/`contains`/`absent` helpers from `hack/verify-hardening.sh` verbatim. The sequence:

```
== harden ==
  apply a WorkloadHardening over the three namespaces, wait for Previewed,
  copy every hash into approvedPlan, wait for Applied, wait for rollouts.
  Assert QoS is Burstable and the filled annotation is present on each.

== AC-U08 ==
  The Deployment's ReplicaSet carries the copied filled annotation:
    kubectl -n undo-open get rs -l app=api -o jsonpath=...filled
  Assert it is non-empty — that is the condition. Then assert the undo's
  status.plan names no ReplicaSet. A fake client never produces this, because
  it does not run the Deployment controller.

== preview ==
  apply the WorkloadHardeningUndo, wait for Previewed.
  Assert: no template changed yet, AND skip/skip-by ARE already on every
  selected workload (AC-U21 — an unarmed undo holds).

== AC-U04 ==
  In undo-restricted: the four always-on fields are reported
  BlockedByPodSecurity and the requests are planned for removal.
  In undo-baseline: all of them are planned.

== AC-U05 ==
  In undo-open: the cpu request is reported BlockedByLimitRange, memory is not.

== AC-U03 ==
  The planned deletion for seccomp names securityContext.seccompProfile, not
  .type. The API server accepted the dry-run — which is the assertion a fake
  client cannot make, because it validates no API type.

== apply (AC-U11) ==
  Copy the hashes in, wait for Applied, wait for rollouts.
  Assert: the rollout completed a second time, pods are Ready, QoS is back to
  BestEffort, the filled annotation is gone where everything reverted and
  carries exactly the survivors where it did not.

== AC-U14 ==
  Re-apply the WorkloadHardening with a bumped approvedPlan. Assert every
  reverted target is reported excluded rather than planned.

== AC-U15 ==
  kubectl delete the undo. Assert it terminates (the finalizer cleared), and
  that skip and skip-by are gone from every target.
```

Every assertion goes through the `check`/`contains`/`absent` helpers so a failure names what it wanted. End with the `fail` check and `AC-U03/U04/U05/U08/U11 PASSED`.

```bash
chmod +x hack/verify-undo.sh
```

- [ ] **Step 3: Add the make targets**

```make
samples-undo:
	kubectl delete namespace undo-open undo-restricted undo-baseline --ignore-not-found --wait
	kubectl apply -f deploy/samples/workloads-undo.yaml
	kubectl -n undo-open rollout status deployment/api --timeout=120s
	kubectl -n undo-open rollout status deployment/worker --timeout=120s
	kubectl -n undo-restricted rollout status deployment/locked --timeout=120s
	kubectl -n undo-baseline rollout status deployment/loose --timeout=120s

verify-undo: deploy samples-undo
	./hack/verify-undo.sh
```

Add both to `.PHONY`, and add `verify-undo` to the `verify` aggregate:

```make
verify: verify-isolation verify-hardening verify-undo
```

- [ ] **Step 4: Run it against a live cluster**

```bash
make kind-up || true
make verify-undo
```

Expected: every line `ok`, and the PASSED banner. Iterate here until it is green — this is the task where the design meets a real admission chain, and AC-U04 is the criterion the spec singles out as passing a dry-run cleanly and halting the rollout afterwards.

- [ ] **Step 5: Confirm the two gates actually fired**

```bash
kubectl -n isolation-system get workloadhardeningundo tenant-rollback \
  -o jsonpath='{range .status.findings[*]}{.namespace}/{.name}: {.reason}{"\n"}{end}' | grep BlockedBy
```

Expected: at least one `BlockedByPodSecurity` from `undo-restricted` and one `BlockedByLimitRange` from `undo-open`. A run where neither appears is a green script that tested nothing.

- [ ] **Step 6: Wire it into CI**

In `.github/workflows/ci.yml`, the `kind` job already runs `make verify-crd` and `make verify`, and Task 2 plus Step 3 above added the undo suites to both aggregates — so the workflow needs no edit. Confirm:

```bash
make -n verify | grep -c verify-undo.sh
make -n verify-crd | grep -c verify-crd-undo.sh
```

Expected: `1` and `1`.

- [ ] **Step 7: Commit**

```bash
git add deploy/samples/workloads-undo.yaml hack/verify-undo.sh Makefile
git commit -m "test(undo): the five criteria a fake client cannot reach

AC-U03's dry-run half, AC-U04, AC-U05, AC-U08 and AC-U11. A fake client runs
no admission plugin, no Deployment controller, and validates no API type — so
it would accept a seccompProfile: {} patch that a real API server refuses on
every target, and report a green AC-U03 all the way to a cluster.

Three namespaces, because the two gates BR-U04 names cannot be produced in
one: enforce: restricted for the Pod Security half, a Container min with no
default for the LimitRange half, and a plain one for the case where everything
is removable. AC-U08 needs a live Deployment controller to copy the annotation
onto a ReplicaSet at all.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Acceptance criteria coverage

The five the spec's NFR-U04 marks script-only, plus AC-U12. A fake client runs neither admission plugin, nor the Deployment controller, nor the CRD's schema and CEL rules, and validates no API type at all.

| ID | Task | Why it cannot be a unit test |
| --- | --- | --- |
| AC-U03 (dry-run half) | 3 | A fake client validates no API type, so it accepts the `seccompProfile: {}` patch a real API server refuses on every target |
| AC-U04 | 3 | Pod Security is an admission plugin; a fake client runs none |
| AC-U05 | 3 | LimitRange minimums are enforced at pod admission, which a fake client never performs |
| AC-U08 | 3 | The `filled` annotation is copied onto a ReplicaSet by the Deployment controller, which a fake client does not run |
| AC-U11 | 3 | Two real rollouts and a real QoS class |
| AC-U12 | 2 | The CRD's structural schema and CEL rules are evaluated by the API server |

## Review Focus coverage

| # | Condition | Task, Step |
| --- | --- | --- |
| 1 | A CI cluster name that disagrees with the Makefile | 1 Step 2 |
| 2 | A green script that tested nothing | 3 Step 5 |
| 3 | A transition rule that rejects everything | 2 Step 4 |

## Deviations and clarifications to confirm before merging

1. **DECLINED — envtest for the CEL rules and structural schemas.** The spec asks for it as item 3, on the ground that the scripts "cover only as far as `kubectl apply` reports". They cover considerably more: `hack/verify-crd-hardening.sh` runs fourteen `expect_reject`/`expect_accept` cases against a real API server and asserts a **specific error substring** for each, with a comment explaining why the substring is load-bearing. That is the assertion envtest would make, against the same admission chain, on a cluster Task 1 now starts anyway. Buying it would mean `sigs.k8s.io/controller-runtime` plus a downloaded control-plane binary — a large new dependency against NFR-01, whose one waiver is `prometheus/client_golang` and is granted for a different reason. Task 2 extends the existing mechanism rather than introducing a second one. **If the spec wants envtest regardless, say so and NFR-01 needs a second waiver.**

2. **`hack/verify-undo.sh` and `hack/verify-crd-undo.sh` live here rather than in the undo plan.** The spec puts them here (items 2 and 3 of this section), and this plan follows it — every `hack/verify-*.sh` in the repository is owned by one document. The cost is real and worth naming: the undo plan ships a CRD whose rejection cases are asserted in a different file, so its Task 4 proves the schema **installs** and this plan's Task 2 proves it **refuses**. If a reviewer would rather each feature carried its own verification, move Tasks 2 and 3 into the undo plan wholesale; nothing else changes.

3. **CI runs the aggregates, not the individual scripts.** Task 1's `kind` job runs `make verify` and `make verify-crd`, which the refactor plan's Task 4 defines. Tasks 2 and 3 add themselves to those aggregates rather than to the workflow, so a fourth feature's suite joins CI by editing the Makefile and nothing else. The metrics plan adds one step to this workflow directly, because a metrics assertion is not a `verify-*` script.

4. **The unit job is not gated on the kind job.** They run in parallel. A broken unit suite and a broken cluster suite are different failures and a reviewer wants both in one pass, not the second one hidden behind the first.
