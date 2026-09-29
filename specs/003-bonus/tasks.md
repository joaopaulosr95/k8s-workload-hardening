# Bonus — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the four items the bonus spec names — a naming and documentation refactor across 001 and 002, an undo for workload hardening, a metrics endpoint, and CI running the existing verification suite on kind — without changing what 001 or 002 do.

**Architecture:** Three parts, in dependency order, in one document because they share a spec. **Part A** is renames plus one function split: no behaviour changes, so 002's existing tests are the proof rather than something to rewrite, and each task is a characterisation cycle — green, rename, green — instead of TDD. **Part B** is the only feature. Its hard half is a pure function in `pkg/plan` that reads the provenance annotation 002 writes: the inverse of every change BR-01 makes is "delete this field", because it only ever fills an absent effective value, so there is no previous value to snapshot. A third reconciler shares the existing binary, queue and single worker; the hold that keeps a reverted workload out of future hardening is two annotations that 002's own exclusion already honours, so **nothing in 002 changes**. **Part C** is a `pkg/metrics` package with a private registry, one call site per counter, and an `http.Server` that shuts down with the controller.

**Tech Stack:** Go 1.27.1 · `k8s.io/api` v0.37.1 · `k8s.io/apimachinery` v0.37.1 · `k8s.io/client-go` v0.37.1 · `k8s.io/klog/v2` v2.140.0 · `github.com/prometheus/client_golang` (Part C only, added under an explicit waiver) · kind v0.32.0 · kubectl v1.36.2 · GitHub Actions · Helm (Task 23 only). Vendored (`vendor/`); Parts A and B add no module dependencies at all.

**Spec:** `specs/003-bonus/spec.md` (read it alongside this plan; every task cites the requirement it implements). 001 and 002 are `specs/001-network-isolation/{spec,tasks}.md` and `specs/002-workload-hardening/{spec,tasks}.md`.

---

## Global Constraints

- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1, exactly as `go.mod` declares.
- **NFR-U01 — no new module dependencies in Parts A and B.** `go.mod` and `go.sum` must be byte-identical before and after Tasks 1–18. Everything the undo needs already lives inside the three `k8s.io` modules and is already in `vendor/`.
- **NFR-01 is waived for exactly one dependency, in Part C: `github.com/prometheus/client_golang`.** Its transitive set comes with it and is accepted; nothing else. **After adding it, run `go mod vendor` and commit the vendor changes in the same commit.** The waiver is the spec's, recorded in its Metrics endpoint section, and does not extend to Parts A or B.
- **No behaviour changes in Part A.** From the spec: "No behaviour changes, so the existing tests are the proof. Anything that needs a test edit beyond a rename is a finding, not a refactor." Every rename task ends with a `git diff` over `*_test.go` that must show only the renamed identifier.
- **Nothing in 002 changes behaviourally, in any part.** BR-U10. The hold is written with `hardening.acme.corp/skip`, which `excluded()` in `pkg/controller/targets.go` already reads. Every existing test in `pkg/controller` must pass unedited except where this plan renames a shared identifier or changes a shared helper's signature.
- **API group/version/kind for the new resource:** `hardening.acme.corp` / `v1alpha1` / `WorkloadHardeningUndo`, plural `workloadhardeningundos`, short name `whu`, namespaced, status subresource.
- **Annotations, exactly:** `hardening.acme.corp/filled` and `hardening.acme.corp/skip` (both existing, both unchanged) and `hardening.acme.corp/skip-by` (new). Finalizer: `hardening.acme.corp/undo-release`. No others.
- **Annotation keys, CRD field names and `json:` tags are on disk in live clusters and are never renamed**, by any task, in any part. Part A renames Go identifiers and make targets only.
- **Caps (FR-U01):** `namespaces` 1–16 unique DNS labels; `workloadSelector` 1–8 `matchLabels` entries with no `matchExpressions`; `approvedPlan` up to 128 twelve-character lowercase hex hashes.
- **Protected namespaces (BR-U06, inheriting BR-05):** reuse the existing `Protected` set and `protectedNamespaces()` in `cmd/main/main.go` — the same flag, not a second one.
- **NFR-U02, in full.** No deletion of a path not recorded by this tool. No deletion of a value a human changed. No deletion admission would make unschedulable. No write before a dry-run of that same patch in the same pass. No patch without its annotation rewrite and its hold in the same request. No removal of a `skip` this tool did not write.
- **NFR-U03 — no new verbs on any core resource.** `deployments`, `statefulsets`, `daemonsets` already carry get/list/patch cluster-wide; `namespaces` and `limitranges` already carry get/list. The only RBAC delta is `workloadhardeningundos`, its `status`, `update` on it for the finalizer, and `workloadhardeningundos/finalizers: update`. Part C adds no RBAC at all — serving metrics reads nothing from the API.
- **Deliberately NOT renamed:** `deployment/network-isolation` and the `isolation-system` namespace. The spec calls this "a decision rather than a rename — it moves an RBAC subject and every script that names it — and skipping it costs nothing but a misleading name." Skipped, and recorded here so it is a decision rather than an omission.
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (`AGENTS.md`). Today's total is 93.5% and the lowest package is 91.7%, so Task 4's floor is satisfied on the commit that introduces it. `cmd/` is wiring and is excluded.
- **`AGENTS.md` role constraint:** do not edit `specs/003-bonus/spec.md`, and do not create or modify anything else under `specs/`. If the implementation needs behaviour the spec does not describe, stop and raise it rather than inventing a requirement. Every such point this plan already found is listed in "Deviations and clarifications to confirm before merging" at the end.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `refactor:`, `docs:`, `build:`, `ci:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Fifteen conditions the spec implies but which no acceptance criterion names, ordered by how likely each is to bite someone using this tool. Each has a test assigned to the task that owns the code. The first nine reach an operator; the last six reach whoever maintains this next, and are listed after them for that reason rather than because they are unlikely.

1. **`capabilities.drop` recorded as the literal `[ALL]`, compared against a live `[]corev1.Capability{"ALL"}`.** The obvious first implementation compares the typed value to the record and never matches. It does not error — every undo then reports every container as edited by a human, reverts nothing, and reports `Applied` having done nothing. AC-U02 names the rendering; nothing names the failure mode, which is silence. Expected: the two renderings agree by construction, because both directions read one table. → **Task 8, Step 1** and **Task 9, Step 2.**
2. **A removal that passes the dry-run cleanly and halts the rollout afterwards.** BR-U04's two gates run against **pods**, not against the workload being patched, so neither appears in a dry-run: the patch succeeds, every new pod is rejected, and the rollout stops. This is 002's AC-05 trap repeated at namespace scope, and the spec says so. Expected: in `enforce: restricted` the four always-on fields are skipped and reported while requests are still removed; a Container `min` with no default blocks that request alone. → **Task 9, Step 2** and **Task 18, Step 5.**
3. **A hold that silently lapses.** An `Applied` object is otherwise inert, so a `skip` someone strips by hand would go unnoticed and the next hardening pass would refill what was just reverted. FR-U05 says "FR-05 unchanged", and FR-05 returns from an `Applied` object before reading anything — the two cannot both hold. Expected: `Applied` is terminal for the **revert** while the hold comparison runs on every pass, and an object whose holds are all intact still issues no writes. → **Task 16, Step 1.**
4. **A malformed provenance annotation.** Hand-edited, truncated by a `kubectl edit`, or carrying a value with an `=` in it. Guessing at it would delete the wrong fields. Expected: one finding naming the object, no deletions, the annotation left exactly as it was, and every other target in the namespace unaffected — and the parser splits on the **first** `=`, because a path never contains one and a value might. → **Task 9, Step 2** and **Task 15, Step 1.**
5. **A `skip-by` whose UID names an object that no longer exists** — a finalizer force-cleared, or an object removed while the controller was down. Without take-over, one orphaned annotation blocks every future bypass of that workload forever and the only remedy is editing it by hand. Expected: it is not a claim, and the next undo that selects the workload takes it. → **Task 13, Step 1** and **Task 14, Step 1.**
6. **A recorded path naming a container that no longer exists, or the wrong list.** `containers[app]` where the container was renamed, or an init container's record read against the regular list — the two may share a name, which is why `Build` keys their paths apart. Expected: "absent", never a panic and never a different container's value. → **Task 8, Step 1.**
7. **A hold patch that reaches `spec`.** The whole argument for not gating the hold behind `approvedPlan` is that two keys under `metadata.annotations` change no pod-template hash and start no rollout. A patch body that touches `spec.template` restarts every pod of every selected workload to write an annotation — the exact harm the feature exists to avoid, performed while previewing. Expected: the metadata-only patch body contains no `spec` key at all. → **Task 10, Step 1** and **Task 16, Step 1.**
8. **An undo of a workload a second `WorkloadHardening` patched after the first.** G-U01: the second annotation write replaced the first, so records only the first object wrote are simply gone. Expected: revert what the annotation says, claim nothing about the rest, and report no finding about records that cannot be known to have existed. → **Task 9, Step 2.**
9. **An unbounded label value on a metric.** `phase` is a closed set of five strings and `resource` is one of three; a namespace or an object name is neither. A counter labelled by object name grows its series count with the cluster and never shrinks, which is how a metrics endpoint becomes the leak it was added to detect. Expected: `Reconcile` takes a `Phase`, not a string, so a caller cannot widen the label set by passing a message. → **Task 19, Step 2.**
10. **A metrics server that ignores `SIGTERM`.** A bare `go http.ListenAndServe` does not see the signal context, so every rollout waits out the kubelet's grace period. Expected: the process exits within a few seconds of `SIGTERM`. → **Task 21, Step 4.**
11. **A Prometheus that scrapes nothing.** Without the operator's CRDs there is no `ServiceMonitor`, so a missing annotation produces an empty panel that reads exactly like a quiet controller. Expected: the install script fails within 60s if no controller target is being scraped, where the cause is still visible. → **Task 23, Step 1.**
12. **A stored `NetworkIsolation` that no longer deserialises after a type rename.** The wire format is the `json:` tags, not the Go type names — but a blanket substitution can reach a tag, and nothing else in the suite would notice. Expected: an object round-trips through `ToUnstructured`/`FromUnstructured` unchanged, asserted **before** the rename and again after. → **Task 1, Step 2.**
13. **A coverage gate that prints a number and exits 0.** `go tool cover -func | tail -1` reports 42% as happily as 94%. A gate that cannot fail is worse than no gate, because it is believed. Expected: the recipe exits non-zero below 90%, verified against a synthetic profile line. → **Task 4, Step 5.**
14. **A finding order that moves between passes.** Findings are sorted and status is written only on a semantic difference, so a split that reorders them would make every resync of every object write status forever — the opposite of FR-06. Expected: three consecutive `discover` calls over the same namespace return byte-identical finding order. → **Task 7, Step 1.**
15. **A CI cluster name that disagrees with the Makefile's.** `kind load docker-image` names the cluster explicitly; a mismatch loads the image into a cluster `kubectl` is not pointing at, and every pod sits in `ErrImageNeverPull` with no error that names the cause. Expected: the two strings are compared in the job that depends on them. → **Task 5, Step 2.**

**Checked and deliberately excluded.** Two undos previewing the same workload is **named** by the spec (BR-U11, "Two undos can both preview the same workload") rather than unnamed, so it is covered by AC-U18 and spends no Review Focus slot. A container name colliding between `containers` and `initContainers` is excluded for 002's reason: the API server enforces uniqueness within a pod, and the change-path format keeps the two lists distinct regardless. A `WorkloadHardeningUndo` racing a `WorkloadHardening` over one workload is excluded because the single worker serialises them — which is asserted at **Task 17, Step 1** as a property of the wiring rather than as a failure mode.

---

## File Structure

| File | Part | Responsibility |
| ---- | ---- | -------------- |
| `pkg/apis/v1alpha1/isolation.go` | A | **Renamed** from `types.go`. `Spec`→`IsolationSpec`, `Status`→`IsolationStatus`. No other change. |
| `pkg/controller/isolation.go` | A | **Renamed** from `reconcile.go`. `Reconciler`→`IsolationReconciler`. No other change. |
| `pkg/plan/plan.go` | A, B | **Modified.** `Policy`→`Request` (A); gains `Enforce`, the namespace's Pod Security label (B). |
| `pkg/controller/targets.go` | A, B | **Modified.** `discoverFindings` split out of `discover` (A); `discover` becomes the free function `discoverWorkloads` with a selector and a skip toggle, and `hardeningTarget` becomes `workloadTarget` carrying the workload's own annotations (B). |
| `Makefile` | A, B, C | **Modified.** Symmetric target names, `verify`/`verify-crd` aggregates, a coverage floor (A); the undo suites (B); `grafana` (C). |
| `.github/workflows/ci.yml` | A, C | **New.** Two jobs: vet plus the unit suite behind the coverage floor, and a kind cluster running `make verify-crd` and `make verify` (A); one metrics assertion (C). |
| `README.md` | A, C | **Modified.** Merged per topic with one Time spent table (A); a Metrics section (C). |
| `pkg/plan/leaf.go` | B | **New.** Pure: resolves one recorded leaf path against a live `PodSpec` and renders it exactly as `Lines` does. The other half of `Build`, and the half that can be wrong quietly. |
| `pkg/plan/invert.go` | B | **New.** Pure: the provenance annotation plus one template plus one namespace's admission facts → the deletions, the findings, and the annotation that replaces it. |
| `pkg/plan/patch.go` | B | **Modified.** `Patch` takes its annotations instead of deriving them; `MetadataPatch` alongside it for the hold. |
| `pkg/plan/resources.go` | B | **Modified.** `Coverage` gains `Floor` — the same LimitRanges BR-06 reads, asked the opposite question. |
| `pkg/apis/v1alpha1/undo.go` | B | **New.** The `WorkloadHardeningUndo` structs, GVR/GVK, the `skip-by` annotation and the finalizer, unstructured conversion. |
| `pkg/apis/v1alpha1/hardening.go` | B | **Modified.** Two `omitempty` holder fields on the shared `TargetStatus`, and the `Reverted` and `Held` outcomes. |
| `pkg/controller/hold.go` | B | **New.** Pure: what to write to claim a target, what to take back on delete, who holds a workload, and which target in a selected set is already someone else's. |
| `pkg/controller/undo.go` | B | **New.** `UndoReconciler`: validation, exclusivity, the inverse plan, the three patch shapes, the re-assert and the release. |
| `pkg/controller/controller.go` | B | **Modified.** A third watched resource on the same queue and the same single worker. |
| `cmd/main/main.go` | B, C | **Modified.** Constructs the third reconciler (B); a `-metrics-addr` flag and an `http.Server` that shuts down with the controller (C). |
| `deploy/crd-undo.yaml` | B | **New.** Structural schema, and the CEL transition rule with the `has()` guards an optional field needs. |
| `deploy/crd-hardening.yaml` | B | **Modified.** One description: it stops telling operators there is no undo, in the commit that makes that false. |
| `deploy/rbac.yaml` | B | **Modified.** The new resource, its status and its finalizers subresource. No new verbs on any core resource. |
| `deploy/samples/undo.yaml`, `deploy/samples/workloads-undo.yaml` | B | **New.** An unarmed undo, and three namespaces producing both of BR-U04's gates plus a plain one. |
| `hack/verify-crd-undo.sh`, `hack/verify-undo.sh` | B | **New.** AC-U12 on a live API server; AC-U03's dry-run half, AC-U04, AC-U05, AC-U08 and AC-U11 on a live cluster. |
| `hack/verify-crd-isolation.sh` | A | **Renamed** from `verify-crd.sh`. Contents unchanged. |
| `pkg/metrics/metrics.go` | C | **New.** A private registry, five counters and one gauge, and the handler. |
| `deploy/metrics-service.yaml` | C | **New.** A Service for the metrics port, and nothing else. |
| `hack/grafana.sh` | C | **New.** The raw `prometheus` and `grafana` charts, scraped by pod annotation, with the queries in the script rather than in a checked-in dashboard. |

---

## Part A — Naming refactor and CI (Tasks 1–7)

Renames and one function split, then the CI that does not exist yet. No behaviour changes anywhere, so every task's proof is the suite that already passes. Part B depends on Task 3 for `plan.Request` and on Task 5 for the workflow it extends; Part C depends on Task 5 and on Task 4's `deploy` recipe.

---

### Task 1: Rename the isolation API types (Refactor — Go identifiers)

**Files:**
- Rename: `pkg/apis/v1alpha1/types.go` → `pkg/apis/v1alpha1/isolation.go`
- Rename: `pkg/apis/v1alpha1/types_test.go` → `pkg/apis/v1alpha1/isolation_test.go`
- Modify: every file referencing `v1alpha1.Spec` / `v1alpha1.Status`

**Interfaces:**
- Consumes: nothing.
- Produces: `v1alpha1.IsolationSpec` (was `Spec`), `v1alpha1.IsolationStatus` (was `Status`). `NetworkIsolation.Spec` and `.Status` **field** names are unchanged — only the types they hold are renamed.

- [ ] **Step 1: Record the green baseline**

```bash
go test ./... 2>&1 | tee /tmp/before.txt
grep -c '^ok' /tmp/before.txt   # expect 4
```

- [ ] **Step 2: Add the round-trip test that pins the wire format (Review Focus 1)**

Append to `pkg/apis/v1alpha1/types_test.go` (before renaming the file):

```go
// The Go type names are ours to change; the JSON field names are on disk in
// every live cluster. This pins the wire format against a rename that reaches
// a struct tag by accident.
func TestIsolationWireFormatIsStable(t *testing.T) {
	iso := &NetworkIsolation{
		ObjectMeta: metav1.ObjectMeta{Name: "pair", Namespace: "isolation-system"},
		Spec: Spec{Peers: []Group{
			{Namespace: "tenant-a", PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "gateway"}}},
			{Namespace: "tenant-b", PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "dashboard"}}},
		}},
		Status: Status{Phase: PhaseEnforced, Message: "both policies written"},
	}

	u, err := ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	for _, path := range [][]string{
		{"spec", "peers"},
		{"status", "phase"},
		{"status", "message"},
	} {
		if _, found, err := unstructured.NestedFieldNoCopy(u.Object, path...); err != nil || !found {
			t.Errorf("wire field %v missing after serialisation (err=%v)", path, err)
		}
	}

	back, err := FromUnstructured(u)
	if err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if !reflect.DeepEqual(iso.Spec, back.Spec) || !reflect.DeepEqual(iso.Status, back.Status) {
		t.Errorf("round trip changed the object:\n got %+v\nwant %+v", back, iso)
	}
}
```

Add `"reflect"` and `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"` to that file's imports if they are not already there. If `ToUnstructured`/`FromUnstructured` carry different names in `types.go`, use the names that are actually there — read the file first.

- [ ] **Step 3: Run the new test to verify it passes before any rename**

Run: `go test ./pkg/apis/v1alpha1/ -run TestIsolationWireFormatIsStable -v`
Expected: PASS. It is a characterisation test — it must be green before the change, so that a failure after the change means the change broke something.

- [ ] **Step 4: Commit the characterisation test on its own**

```bash
git add pkg/apis/v1alpha1/types_test.go
git commit -m "test(isolation): pin the wire format before the type rename"
```

- [ ] **Step 5: Rename the types**

```bash
git mv pkg/apis/v1alpha1/types.go pkg/apis/v1alpha1/isolation.go
git mv pkg/apis/v1alpha1/types_test.go pkg/apis/v1alpha1/isolation_test.go
```

Then, across `pkg/` and `cmd/` only (never `vendor/`), replace the two identifiers. Use word boundaries — a bare `s/Spec/IsolationSpec/` would destroy `HardeningSpec`, `PodSpec` and every `spec` JSON tag:

```bash
files=$(grep -rl --include='*.go' -E '\bv1alpha1\.(Spec|Status)\b|^type (Spec|Status) struct' pkg cmd)
perl -pi -e 's/\bv1alpha1\.Spec\b/v1alpha1.IsolationSpec/g; s/\bv1alpha1\.Status\b/v1alpha1.IsolationStatus/g' $files
perl -pi -e 's/^type Spec struct/type IsolationSpec struct/; s/^type Status struct/type IsolationStatus struct/' pkg/apis/v1alpha1/isolation.go
```

Inside `pkg/apis/v1alpha1` the types are referenced unqualified. Fix those by hand — `grep -n '\bSpec\b\|\bStatus\b' pkg/apis/v1alpha1/*.go` and change only the ones that name these two types, never the `NetworkIsolation.Spec` / `.Status` struct fields and never a `json:` tag.

- [ ] **Step 6: Verify the build and the full suite**

Run: `go build ./... && go test ./... 2>&1 | tee /tmp/after.txt`
Expected: all four packages `ok`, identical to `/tmp/before.txt` modulo timings.

- [ ] **Step 7: Verify no test changed beyond a rename**

```bash
git diff -- '*_test.go' | grep '^[+-]' | grep -v '^[+-][+-]' | grep -v 'IsolationSpec\|IsolationStatus\|v1alpha1\.Spec\|v1alpha1\.Status'
```

Expected: **no output**. Any line here is a behaviour change hiding in a rename, which the spec calls a finding, not a refactor. Stop and report it.

- [ ] **Step 8: Verify no struct tag moved**

```bash
git diff -- pkg/apis/v1alpha1/isolation.go | grep '^[+-]' | grep 'json:'
```

Expected: **no output**.

- [ ] **Step 9: Commit**

```bash
git add -A pkg cmd
git commit -m "refactor(isolation): Spec and Status become IsolationSpec and IsolationStatus

types.go was named for a project with one feature in it. 002 took the
prefixed names and left 001 holding the unqualified ones, so the file that
defines the isolation API reads as if it defines the project's.

No behaviour change: the JSON tags, the CRD and the annotation keys are
untouched, and the existing tests pass unedited beyond the rename.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Rename the isolation reconciler (Refactor — Go identifiers)

**Files:**
- Rename: `pkg/controller/reconcile.go` → `pkg/controller/isolation.go`
- Rename: `pkg/controller/reconcile_test.go` → `pkg/controller/isolation_test.go`
- Modify: `pkg/controller/validate.go`, `pkg/controller/controller.go`, `pkg/controller/hardening.go`, `cmd/main/main.go`, remaining `*_test.go`

**Interfaces:**
- Consumes: `v1alpha1.IsolationSpec`, `v1alpha1.IsolationStatus` from Task 1.
- Produces: `controller.IsolationReconciler` (was `Reconciler`). `(*HardeningReconciler).setStatus` (was `setHardeningStatus`) — legal because the two `setStatus` methods now hang off distinct receivers.

- [ ] **Step 1: Rename the files**

```bash
git mv pkg/controller/reconcile.go pkg/controller/isolation.go
git mv pkg/controller/reconcile_test.go pkg/controller/isolation_test.go
```

- [ ] **Step 2: Rename the reconciler type**

```bash
perl -pi -e 's/\bReconciler\b/IsolationReconciler/g' $(grep -rl --include='*.go' '\bReconciler\b' pkg cmd)
```

`HardeningReconciler` contains `Reconciler` but not as a word boundary match at the start — `\bReconciler\b` does **not** match inside `HardeningReconciler`, because `g` and `R` are both word characters. Verify:

```bash
grep -rn 'HardeningIsolationReconciler\|IsolationIsolationReconciler' pkg cmd
```

Expected: **no output**. If there is any, revert with `git checkout -- pkg cmd` and redo the substitution more narrowly.

- [ ] **Step 3: Rename setHardeningStatus**

```bash
perl -pi -e 's/\bsetHardeningStatus\b/setStatus/g' $(grep -rl --include='*.go' 'setHardeningStatus' pkg)
```

- [ ] **Step 4: Build and run the full suite**

Run: `go build ./... && go test ./... -race`
Expected: all four packages `ok`.

- [ ] **Step 5: Verify no test changed beyond a rename**

```bash
git diff -- '*_test.go' | grep '^[+-]' | grep -v '^[+-][+-]' | grep -v 'IsolationReconciler\|setStatus'
```

Expected: **no output**.

- [ ] **Step 6: Commit**

```bash
git add -A pkg cmd
git commit -m "refactor(isolation): Reconciler becomes IsolationReconciler, and setHardeningStatus loses its prefix

Reconciler vs HardeningReconciler had the same asymmetry as the types: the
unqualified name went to whichever feature was written first rather than to
whichever one is more general, and neither is.

setHardeningStatus comes free with it. The prefix existed only to avoid a
collision with 001's setStatus, and the receivers are distinct now.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Rename `plan.Policy` to `plan.Request` (Refactor — Go identifiers)

**Files:**
- Modify: `pkg/plan/plan.go:83`, `pkg/plan/resources.go`, `pkg/plan/plan_test.go`, `pkg/plan/resources_test.go`, `pkg/controller/hardening.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `plan.Request` (was `plan.Policy`), same fields: `Requests corev1.ResourceList`, `ReadOnlyRootFilesystem bool`, `Coverage Coverage`. `plan.Build(pod *corev1.PodSpec, req Request) Plan` — the parameter is renamed from `policy` to `req` throughout.

This is the one rename with a reason beyond symmetry: `plan.Policy` and the package `pkg/policy` are unrelated things sharing a word, and `pkg/policy` builds NetworkPolicies while `plan.Policy` is a hardening request. The package names stay as they are.

- [ ] **Step 1: Rename the type and the parameter**

```bash
perl -pi -e 's/\bplan\.Policy\b/plan.Request/g' $(grep -rl --include='*.go' 'plan\.Policy' pkg cmd)
perl -pi -e 's/^type Policy struct/type Request struct/; s/\bpolicy Policy\b/req Request/g; s/\bpolicy\./req./g; s/\bpolicy\b/req/g' pkg/plan/plan.go pkg/plan/resources.go
perl -pi -e 's/\bPolicy\{/Request{/g; s/\) Policy \{/) Request {/g; s/\bpolicy Policy\b/req Request/g' pkg/plan/plan_test.go pkg/plan/resources_test.go
```

- [ ] **Step 2: Fix what the substitution missed by hand**

Run `go build ./... 2>&1 | head -20` and fix each error. Expect leftovers in doc comments where "policy" is the English word and must stay — for example `pkg/plan/plan.go`'s package comment "turns one workload template and one policy into the set of fields", and `resources.go`'s references to `SecurityPolicy` / `ResourcePolicy`, which are CRD types in `v1alpha1` and are **not** renamed.

Read every changed comment and restore the English word where the substitution replaced it. `grep -n '\breq\b' pkg/plan/*.go` and check each hit reads as code, not prose.

- [ ] **Step 3: Verify the change hash did not move**

Run: `go test ./pkg/plan/ -run 'TestHash|TestCanonical|TestProvenance' -v`
Expected: PASS. `Canonical` and `Hash` are computed over paths and values, never over a type name, so a rename cannot move them — this step exists to prove it rather than assume it, because a moved hash invalidates every `approvedPlan` in every live cluster.

- [ ] **Step 4: Run the full suite**

Run: `go test ./... -race`
Expected: all four packages `ok`.

- [ ] **Step 5: Verify no test changed beyond a rename**

```bash
git diff -- '*_test.go' | grep '^[+-]' | grep -v '^[+-][+-]' | grep -v 'Request\|req\b'
```

Expected: **no output**.

- [ ] **Step 6: Commit**

```bash
git add -A pkg
git commit -m "refactor(plan): Policy becomes Request

plan.Policy and pkg/policy are unrelated things sharing a word: one is a
hardening request, the other builds NetworkPolicies. The package names stay
— the pkg/policy / pkg/plan asymmetry is cosmetic next to a type and a
package that mean different things by the same name.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Symmetric make targets and a coverage floor (Refactor — build and manifests)

**Files:**
- Rename: `hack/verify-crd.sh` → `hack/verify-crd-isolation.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: nothing.
- Produces: make targets `verify-isolation`, `samples-isolation`, `verify-crd-isolation`, and aggregates `verify` (runs both features) and `verify-crd` (runs both CRD suites). Task 5 calls `make verify` and `make verify-crd`.

- [ ] **Step 1: Rename the script**

```bash
git mv hack/verify-crd.sh hack/verify-crd-isolation.sh
```

- [ ] **Step 2: Rewrite the Makefile targets**

Replace the `.PHONY` line and everything from `samples:` to the end of `Makefile` with:

```make
.PHONY: test cover image kind-up kind-down deploy \
        samples-isolation verify-isolation verify-crd-isolation \
        samples-hardening verify-hardening verify-crd-hardening \
        verify verify-crd

samples-isolation:
	kubectl apply -f deploy/samples/workloads.yaml
	kubectl -n tenant-a wait --for=condition=Ready pod/gateway --timeout=120s
	kubectl -n tenant-b wait --for=condition=Ready pod/dashboard --timeout=120s
	kubectl -n tenant-c wait --for=condition=Ready pod/bystander --timeout=120s

verify-isolation: deploy samples-isolation
	./hack/verify-isolation.sh

verify-crd-isolation:
	./hack/verify-crd-isolation.sh

samples-hardening:
	# From a clean slate: the tool's own annotations and patches survive a
	# re-apply, and verify-hardening asserts that a preview has written
	# nothing yet, so a second run would read the first run's results.
	kubectl delete namespace harden-a harden-b --ignore-not-found --wait
	kubectl apply -f deploy/samples/workloads-hardening.yaml
	kubectl -n harden-a rollout status deployment/fill-me --timeout=120s
	kubectl -n harden-a rollout status deployment/nonroot --timeout=120s
	kubectl -n harden-a rollout status deployment/already-hardened --timeout=120s
	kubectl -n harden-a rollout status deployment/skip-me --timeout=120s
	kubectl -n harden-b rollout status deployment/covered --timeout=120s

verify-hardening: deploy samples-hardening
	./hack/verify-hardening.sh

verify-crd-hardening:
	./hack/verify-crd-hardening.sh

# Both features in one invocation. make runs `deploy` once however many
# targets name it, so the image is built and loaded a single time — which is
# what the CI job in specs/003-bonus/spec.md needs.
verify: verify-isolation verify-hardening

verify-crd: verify-crd-isolation verify-crd-hardening
```

- [ ] **Step 3: Add the coverage floor to `cover` (Review Focus 4)**

Replace the `cover:` recipe with:

```make
cover:
	go test ./pkg/... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1
	@go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/,"",$$3); if ($$3+0 < 90) {printf "coverage %s%% is below the 90%% floor in AGENTS.md\n", $$3; exit 1}}'
```

Today's total is 93.5% and the lowest package is 91.7%, so the floor is satisfied on the commit that introduces it.

- [ ] **Step 4: Verify the aggregate resolves and deploys once (Review Focus 2)**

Run: `make -n verify | grep -c 'docker build'`
Expected: `1`. `make -n` prints the recipe without running it; a `2` means `deploy` is being run per aggregate leaf and the second run would race the first one's rollout.

- [ ] **Step 5: Verify the coverage gate actually fails**

```bash
printf 'total:\t\t\t(statements)\t42.0%%\n' | awk '/^total:/ {gsub(/%/,"",$3); if ($3+0 < 90) {printf "coverage %s%% is below the 90%% floor in AGENTS.md\n", $3; exit 1}}'; echo "exit=$?"
```

Expected: prints the message and `exit=1`. Then confirm the real one passes:

Run: `make cover`
Expected: prints `total: ... 93.5%` or higher and exits 0.

- [ ] **Step 6: Verify every script the Makefile names exists**

```bash
for s in $(grep -o 'hack/[a-z-]*\.sh' Makefile | sort -u); do test -x "$s" || echo "MISSING or not executable: $s"; done
```

Expected: **no output**.

- [ ] **Step 7: Commit**

```bash
git add -A Makefile hack
git commit -m "build: symmetric make targets, and a verify that runs both features

make verify ran isolation and make verify-hardening ran hardening: the same
asymmetry as the Go names, one level out. The 001 side takes its prefix, and
verify and verify-crd become aggregates — which is what the CI job needs, and
make runs the shared deploy prerequisite once.

cover now fails below the 90% floor AGENTS.md asks for rather than printing a
number nobody reads. Today: 93.5%.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: CI on every push (002 G-06)

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `make test`, `make cover`, `make verify`, `make verify-crd` from Task 4.
- Produces: a workflow named `ci` with jobs `unit` and `kind`. Part B adds the undo suites to the `verify` and `verify-crd` aggregates this job already runs, so it needs no further edit.

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

### Task 6: Merge the README (Refactor — docs)

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: the make target names from Task 4.
- Produces: nothing other tasks read.

The README was written for 001 and 002 was appended to it. `# core task 2` sits at line 54 inside `## Setup`, and lines 269–403 duplicate the whole structure: Decisions, Limitations, What I'd do and Time spent at 297/334/363/391 mirroring 98/166/200/263.

- [ ] **Step 1: Merge per topic**

Restructure to a single spine, both features present in each section:

```
# k8s-workload-hardening
## What it does          — both features, two paragraphs
## Setup                 — one path; the stray `# core task 2` heading is deleted
## Tested versions
## How it works          — ### Network isolation (001), ### Workload hardening (002)
## Decisions             — merged list, each entry tagged (001) or (002) where it matters
## Status                — the phases, shared vocabulary noted once
## Limitations           — merged
## What I'd do with more time — merged
## Tests                 — the make targets from Task 4, by their new names
## Time spent            — ONE table, both features as rows
## Feedback on the brief
```

Delete the `# core task 2` heading at line 54 and the `## Core task 2 — on-demand workload hardening` block at 269, folding their content into the sections above. Keep every fact; the merge is structural.

- [ ] **Step 2: Update every make invocation the README names**

```bash
grep -n 'make \(verify\|samples\|verify-crd\)\b' README.md
```

Each hit must now read `make verify-isolation`, `make samples-isolation` or `make verify-crd-isolation` if it means the 001 suite, or stay as `make verify` / `make verify-crd` if it means both. Fix each by hand — a blanket substitution would get the aggregates wrong.

- [ ] **Step 3: Verify the duplicate structure is gone**

```bash
for h in Decisions Limitations "What I'd do" "Time spent"; do
  printf '%-16s %s\n' "$h" "$(grep -c "^#\+ .*$h" README.md)"
done
```

Expected: `1` for every one of the four.

- [ ] **Step 4: Verify no command in the README is stale**

```bash
for t in $(grep -o 'make [a-z-]*' README.md | sort -u | cut -d' ' -f2); do
  make -n "$t" >/dev/null 2>&1 || echo "README names a make target that does not exist: $t"
done
```

Expected: **no output**.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs: one README per topic instead of one per core task

002 was appended rather than merged: a stray '# core task 2' heading inside
## Setup, and 135 lines duplicating the whole structure — Decisions,
Limitations, What I'd do and Time spent, twice each. Merged per topic with
both features in every section and a single Time spent table, and the make
targets updated to the names Task 4 gave them.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Split the reporting half out of `discover` (Refactor — architecture)

**Files:**
- Modify: `pkg/controller/targets.go:145-310`
- Modify: `pkg/controller/targets_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `(*HardeningReconciler).discoverFindings(ctx context.Context, namespace string) ([]v1alpha1.Finding, error)` — the read-only half. `discover` keeps its signature `(ctx, namespace) ([]hardeningTarget, []v1alpha1.Finding, error)` and calls it.

`discover` is 165 lines and the only function in the non-test code the line counts single out. Its second half — ReplicaSets, Jobs, CronJobs and bare Pods — is pure reporting: it produces findings, never targets, and shares nothing with the first half but the `report` closure. The three workload blocks are **not** extracted: they differ in their refusal check (`paused` / `OnDelete` / `partition`) and their replica source (`Status.Replicas` / `Status.Replicas` / `Status.DesiredNumberScheduled`), so a shared helper would take three function parameters to save nine lines and be harder to read at 3am than the repetition it replaced.

- [ ] **Step 1: Write the failing test that pins finding order (Review Focus 5)**

Add to `pkg/controller/targets_test.go`:

```go
// Findings are sorted by kind then name, and setStatus writes status only on a
// semantic difference. A split that reorders them would make every resync of
// every object write status forever, which is the opposite of FR-06.
func TestDiscoverFindingOrderIsStable(t *testing.T) {
	r := newHardener(
		ns("tenant-a"),
		deployment("tenant-a", "api"),
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "orphan-rs", Namespace: "tenant-a"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "tenant-a"}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "tenant-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "tenant-a"}},
	)

	var first []string
	for pass := 0; pass < 3; pass++ {
		_, findings, err := r.discover(context.Background(), "tenant-a")
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		var got []string
		for _, f := range findings {
			got = append(got, f.Kind+"/"+f.Name)
		}
		if pass == 0 {
			first = got
			want := []string{"CronJob/nightly", "Job/migrate", "Pod/bare", "ReplicaSet/orphan-rs"}
			if !slices.Equal(got, want) {
				t.Fatalf("findings = %v, want %v", got, want)
			}
			continue
		}
		if !slices.Equal(got, first) {
			t.Errorf("pass %d reordered findings:\n got %v\nwant %v", pass, got, first)
		}
	}
}
```

Add `"slices"` to the imports if absent. `ns` is the existing namespace helper in this file — read it and use whatever it is actually called.

- [ ] **Step 2: Run it to verify it passes before the split**

Run: `go test ./pkg/controller/ -run TestDiscoverFindingOrderIsStable -v`
Expected: PASS. A characterisation test again — green before, green after, or the split changed behaviour.

- [ ] **Step 3: Extract the reporting half**

In `pkg/controller/targets.go`, move the ReplicaSet, Job, CronJob and Pod blocks — everything from `replicaSets, err := apps.ReplicaSets(...)` up to but not including the two `slices.SortFunc` calls — into a new method placed directly after `discover`:

```go
// discoverFindings enumerates everything in one namespace this tool reports
// but never patches: ReplicaSets and Jobs whose owner is the target instead,
// standalone ones that are out of scope, CronJobs, and bare pods. All four
// lists are served from the API server's watch cache — a slightly stale
// observation is still a true one, and the next resync corrects it (BR-04).
//
// Split out of discover because it shares nothing with the targeting half: it
// produces findings, never targets, and reaches four resources the patching
// path never looks at.
func (r *HardeningReconciler) discoverFindings(ctx context.Context, namespace string) ([]v1alpha1.Finding, error) {
	var findings []v1alpha1.Finding
	report := func(kind, name, reason string) {
		findings = append(findings, v1alpha1.Finding{Namespace: namespace, Kind: kind, Name: name, Reason: reason})
	}

	// ... the four blocks, moved verbatim, each returning (nil, err) on a
	// list failure instead of (nil, nil, err) ...

	return findings, nil
}
```

Then, in `discover`, replace the removed blocks with:

```go
	reported, err := r.discoverFindings(ctx, namespace)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, reported...)
```

Leave both `slices.SortFunc` calls in `discover`, after the append — the sort must see the combined list, which is what keeps the order in the test above stable.

- [ ] **Step 4: Run the order test and the full suite**

Run: `go test ./pkg/controller/ -run TestDiscoverFindingOrderIsStable -v && go test ./... -race`
Expected: PASS, then all four packages `ok`.

- [ ] **Step 5: Verify discover is now roughly half its size**

```bash
awk '/^func \(r \*HardeningReconciler\) discover\(/{s=NR} s && /^}/{print "discover: " NR-s+1 " lines"; exit}' pkg/controller/targets.go
```

Expected: under 100 lines, down from 166.

- [ ] **Step 6: Verify no other test changed**

```bash
git diff -- pkg/controller/targets_test.go | grep '^-' | grep -v '^---'
```

Expected: **no output** — the task only adds a test, it deletes nothing.

- [ ] **Step 7: Commit**

```bash
git add pkg/controller/targets.go pkg/controller/targets_test.go
git commit -m "refactor(hardening): split the reporting half out of discover

discover was 166 lines and the only function in the non-test code the line
counts single out. Its second half shares nothing with the first: four
resources the patching path never reads, producing findings and never targets.

The three workload blocks stay where they are. They differ in their refusal
check and their replica source, so a shared helper would take three function
parameters to save nine lines and read worse at 3am than the repetition.

Adds a characterisation test for finding order first: findings are sorted and
status is written only on a semantic difference, so a reordering would make
every resync write status forever.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

---

## Part B — Undo for workload hardening (Tasks 8–18)

The only feature in this spec. The pure half comes first — the live-value reader, the inverse plan, the patch body — so the hard decisions are testable before any client is involved, which is 002's shape and most of why this is cheap. Depends on Task 3 for `plan.Request`; if Part A has not run, the argument is `plan.Policy` and nothing else changes, as FR-U02 says.

---

### Task 8: The live-value reader (FR-U02, BR-U02, AC-U02)

**Files:**
- Create: `pkg/plan/leaf.go`
- Create: `pkg/plan/leaf_test.go`

**Interfaces:**
- Consumes: `templatePath`, `containersField`, `initContainersField`, `cutList` from `pkg/plan`.
- Produces: `plan.ReadLeaf(pod *corev1.PodSpec, path string) (value string, present bool)` — resolves a recorded leaf path against a live PodSpec and renders it exactly as `Lines` would. Task 9 is its only caller.

This is the piece FR-U02 warns about: `Build` renders only fields it is about to write, so nothing existing can render a field that is already set. A renderer that drifts from `Build`'s produces a *wrong answer* rather than an error — it leaves a field in place, reported as "edited by a human", and the undo quietly does nothing.

- [ ] **Step 1: Write the failing test**

Create `pkg/plan/leaf_test.go`:

```go
package plan

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// hardened is a template carrying exactly what Build would have written into
// an empty one, so every record ReadLeaf can meet is present on it.
func hardened() *corev1.PodSpec {
	yes := true
	no := false
	return &corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   &yes,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		InitContainers: []corev1.Container{{
			Name: "setup",
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &no,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}},
		Containers: []corev1.Container{{
			Name: "app",
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &no,
				ReadOnlyRootFilesystem:   &yes,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			}},
		}},
	}
}

// AC-U02 and Review Focus 1. Every leaf shape Build can write reads back
// through ReadLeaf as the same string Lines produced — capabilities.drop
// included, which is the one that does not compare as a Go value. Comparing a
// []corev1.Capability to the record is the obvious first mistake and it fails
// silently: every container reports as edited by a human and nothing reverts.
func TestReadLeafRendersExactlyWhatBuildWrote(t *testing.T) {
	pod := hardened()

	// The round trip is the assertion: run Build against an empty template,
	// take every line it would have written, and read each one back off a
	// template that already has it.
	empty := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup"}},
		Containers:     []corev1.Container{{Name: "app"}},
	}
	built := Build(empty, Request{
		Requests:               requests("10m", "32Mi"),
		ReadOnlyRootFilesystem: true,
	})
	if len(built.Changes) == 0 {
		t.Fatal("Build produced no changes; the fixture is wrong")
	}

	for _, c := range built.Changes {
		got, ok := ReadLeaf(pod, c.Path)
		if !ok {
			t.Errorf("ReadLeaf(%s) reported absent; Build writes it and the fixture has it", c.Path)
			continue
		}
		if got != c.Value {
			t.Errorf("ReadLeaf(%s) = %q, Build wrote %q — the two renderings have drifted", c.Path, got, c.Value)
		}
	}
}

// Review Focus 2: a path naming something that is not there answers absent.
// Not a panic, and never a different container's value.
func TestReadLeafAbsent(t *testing.T) {
	pod := hardened()
	for _, path := range []string{
		"spec.template.spec.containers[gone].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.containers[setup].securityContext.allowPrivilegeEscalation", // init container, wrong list
		"spec.template.spec.initContainers[app].securityContext.capabilities.drop",      // regular container, wrong list
		"spec.template.spec.containers[app].resources.requests.ephemeral-storage",       // never written
		"spec.template.spec.containers[setup].resources.requests.cpu",                   // container has no requests
		"spec.template.spec.securityContext.runAsUser",                                  // not a field this tool writes
		"spec.template.spec.containers[app].securityContext.readOnlyRootFilesystem.deep", // too many segments
		"",
		"garbage",
	} {
		if v, ok := ReadLeaf(pod, path); ok {
			t.Errorf("ReadLeaf(%q) = %q, true; want absent", path, v)
		}
	}
}

// A pod-level field absent from a template that has a securityContext, and one
// absent from a template that has none, are both absent and neither panics.
func TestReadLeafNilSafety(t *testing.T) {
	bare := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	for _, path := range []string{
		"spec.template.spec.securityContext.runAsNonRoot",
		"spec.template.spec.securityContext.seccompProfile.type",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.containers[app].securityContext.capabilities.drop",
		"spec.template.spec.containers[app].resources.requests.cpu",
	} {
		if _, ok := ReadLeaf(bare, path); ok {
			t.Errorf("ReadLeaf(%q) on a bare template reported present", path)
		}
	}
	if _, ok := ReadLeaf(nil, "spec.template.spec.securityContext.runAsNonRoot"); ok {
		t.Error("ReadLeaf(nil, ...) reported present")
	}
}

// A human's later edit reads back as their value, which is what BR-U02
// compares against the record to decide whether to leave the field alone.
func TestReadLeafSeesHumanEdits(t *testing.T) {
	pod := hardened()
	pod.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("500m")
	pod.Containers[0].SecurityContext.Capabilities.Drop = []corev1.Capability{"NET_RAW"}

	if got, _ := ReadLeaf(pod, "spec.template.spec.containers[app].resources.requests.cpu"); got != "500m" {
		t.Errorf("cpu = %q, want 500m", got)
	}
	if got, _ := ReadLeaf(pod, "spec.template.spec.containers[app].securityContext.capabilities.drop"); got != "[NET_RAW]" {
		t.Errorf("drop = %q, want [NET_RAW]", got)
	}
}
```

`requests(...)` is the existing helper in `pkg/plan/resources_test.go`. `Request` is `Policy` if Part A has not run.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/plan/ -run TestReadLeaf -v`
Expected: FAIL — `undefined: ReadLeaf`.

- [ ] **Step 3: Write the implementation**

Create `pkg/plan/leaf.go`:

```go
package plan

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ReadLeaf resolves one recorded leaf path against a live pod template and
// renders the value it finds in the same form Lines produces. The second
// result is false when the path names nothing on this template.
//
// It is the other half of Build, and the half that can be wrong quietly.
// Build renders only fields it is about to write; an undo reads fields that
// are already set, so nothing in Build can answer this question. A renderer
// that drifts from Build's does not error — it returns a string that does not
// equal the record, BR-U02 concludes a human edited the field, and the undo
// reports success having reverted nothing.
//
// The rendering rules, one per leaf Build can write:
//
//	runAsNonRoot, allowPrivilegeEscalation, readOnlyRootFilesystem  "true"/"false"
//	seccompProfile.type                                            the type verbatim
//	capabilities.drop                                              fmt.Sprint of the list: [ALL]
//	resources.requests.<resource>                                  Quantity.String()
//
// Anything else is absent by construction: this tool writes six fields, so a
// record naming a seventh did not come from here.
func ReadLeaf(pod *corev1.PodSpec, path string) (string, bool) {
	if pod == nil {
		return "", false
	}
	rest, ok := strings.CutPrefix(path, templatePath+".")
	if !ok {
		return "", false
	}

	field, key, isList := cutList(segment(rest))
	if !isList {
		return readPodLeaf(pod.SecurityContext, rest)
	}
	if field != containersField && field != initContainersField {
		return "", false
	}
	c := findContainer(pod, field, key)
	if c == nil {
		return "", false
	}
	return readContainerLeaf(c, strings.TrimPrefix(rest, segment(rest)+"."))
}

// segment returns the first dot-separated segment of path.
func segment(path string) string {
	if i := strings.IndexByte(path, '.'); i >= 0 {
		return path[:i]
	}
	return path
}

// findContainer returns the named container from the named list, or nil. The
// list is part of the lookup rather than a fallback: an init container and a
// regular one may share a name, and Build keys their paths apart for exactly
// that reason.
func findContainer(pod *corev1.PodSpec, list, name string) *corev1.Container {
	in := pod.Containers
	if list == initContainersField {
		in = pod.InitContainers
	}
	for i := range in {
		if in[i].Name == name {
			return &in[i]
		}
	}
	return nil
}

// readPodLeaf renders a pod-level field. rest is the path below the template.
func readPodLeaf(sc *corev1.PodSecurityContext, rest string) (string, bool) {
	if sc == nil {
		return "", false
	}
	switch rest {
	case "securityContext.runAsNonRoot":
		return renderBool(sc.RunAsNonRoot)
	case "securityContext.seccompProfile.type":
		if sc.SeccompProfile == nil {
			return "", false
		}
		return string(sc.SeccompProfile.Type), true
	default:
		return "", false
	}
}

// readContainerLeaf renders a container-level field. rest is the path below
// the container segment.
func readContainerLeaf(c *corev1.Container, rest string) (string, bool) {
	if resource, ok := strings.CutPrefix(rest, "resources.requests."); ok {
		q, present := c.Resources.Requests[corev1.ResourceName(resource)]
		if !present {
			return "", false
		}
		// A local copy: Quantity.String has a pointer receiver, so a map value
		// cannot be rendered in place. Same reason as buildResources.
		return q.String(), true
	}

	sc := c.SecurityContext
	if sc == nil {
		return "", false
	}
	switch rest {
	case "securityContext.allowPrivilegeEscalation":
		return renderBool(sc.AllowPrivilegeEscalation)
	case "securityContext.readOnlyRootFilesystem":
		return renderBool(sc.ReadOnlyRootFilesystem)
	case "securityContext.capabilities.drop":
		if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
			return "", false
		}
		// fmt.Sprint of a one-element list is "[ALL]", which is the literal
		// Build writes at plan.go's capabilities case. The two renderings
		// agreeing is what AC-U02 tests.
		return fmt.Sprint(sc.Capabilities.Drop), true
	default:
		return "", false
	}
}

// renderBool renders an optional bool the way Build does.
func renderBool(b *bool) (string, bool) {
	if b == nil {
		return "", false
	}
	if *b {
		return "true", true
	}
	return "false", true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/plan/ -run TestReadLeaf -v`
Expected: all four tests PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go test ./... -race && make cover`
Expected: every package `ok`, coverage at or above 90%.

- [ ] **Step 6: Commit**

```bash
git add pkg/plan/leaf.go pkg/plan/leaf_test.go
git commit -m "feat(plan): ReadLeaf, the other half of Build

An undo compares the recorded value to the live one, and nothing in plan could
render a field that is already set: Build renders only what it is about to
write. So ReadLeaf, six leaf shapes and one switch.

The rendering has to match Build's exactly or it fails silently — a drifted
renderer returns a string that does not equal the record, BR-U02 concludes a
human edited the field, and the undo reports success having reverted nothing.
capabilities.drop is where that bites: the record is the literal [ALL] and the
live value is a []corev1.Capability, which never compares equal as a Go value.

The test asserts the round trip rather than a fixture: every line Build writes
into an empty template reads back off a hardened one as the same string.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: The inverse plan (FR-U02, BR-U02, BR-U03, BR-U04, AC-U02, AC-U03)

**Files:**
- Create: `pkg/plan/invert.go`
- Create: `pkg/plan/invert_test.go`
- Modify: `pkg/plan/plan.go` (add `Enforce` to `Request`)
- Modify: `pkg/plan/resources.go` (add `Floor` to `Coverage`)
- Modify: `pkg/plan/resources_test.go`

**Interfaces:**
- Consumes: `ReadLeaf` from Task 8.
- Produces:
  - `plan.Invert(pod *corev1.PodSpec, provenance string, req Request) (Plan, string)` — deletions in `Changes`, everything skipped in `Findings`, and the rewritten annotation value as the second result (`""` means remove the annotation). Task 14 is its only caller.
  - `plan.Request.Enforce string` — the namespace's `pod-security.kubernetes.io/enforce` label.
  - `plan.Coverage.Floor(r corev1.ResourceName) string` — the LimitRange that would reject a pod with no request for `r`.
  - Finding reason prefixes `EditedSinceHardening:`, `BlockedByPodSecurity:`, `BlockedByLimitRange:`, `NoRecord:`, `Unparseable:` (FR-U06).

- [ ] **Step 1: Extend Request and Coverage**

In `pkg/plan/plan.go`, add to `Request`:

```go
	// Enforce is the namespace's pod-security.kubernetes.io/enforce label, or
	// "". Build ignores it; Invert gates on it (BR-U04). It rides on Request
	// rather than on a second argument because the controller already builds
	// one of these per namespace and this is one more thing it reads there.
	Enforce string
```

In `pkg/plan/resources.go`, add a `floor` map to `Coverage`, initialise it in `Cover`, add the accessor:

```go
// Floor names the LimitRange that would reject a pod with no request for r, or
// "". A Container-scoped min with neither default nor defaultRequest is
// enforced at pod admission against a request that is not there — which is the
// same object BR-06 reads, asked the opposite question (BR-U04).
func (c Coverage) Floor(r corev1.ResourceName) string { return c.floor[r] }
```

And, in `Cover`, between the existing first and second passes:

```go
	// Third question of the same objects: which resources have a min that
	// nothing defaults. Removing a request this tool filled would make every
	// new pod inadmissible in such a namespace. It has to run after the first
	// pass, because a resource something else defaults is not one this matters
	// for — the API server supplies the value and the min is satisfied.
	for i := range sorted {
		for _, item := range containerItems(&sorted[i]) {
			for _, r := range names {
				if cov.request[r] != "" {
					continue
				}
				if _, ok := item.Min[r]; ok {
					claim(cov.floor, r, sorted[i].Name)
				}
			}
		}
	}
```

- [ ] **Step 2: Write the failing test**

Create `pkg/plan/invert_test.go`:

```go
package plan

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// record is what Build would have written into an empty template, rendered as
// the annotation stores it.
func record(pod *corev1.PodSpec) string {
	return Provenance(Build(pod, Request{Requests: requests("10m", "32Mi"), ReadOnlyRootFilesystem: true}).Changes)
}

func deletionPaths(p Plan) []string {
	out := make([]string, 0, len(p.Changes))
	for _, c := range p.Changes {
		out = append(out, c.Path)
	}
	return out
}

func reasons(p Plan) string {
	var b strings.Builder
	for _, f := range p.Findings {
		b.WriteString(f.Container + ":" + f.Reason + "\n")
	}
	return b.String()
}

// AC-U01: a hardened template inverts to a deletion of everything this tool
// wrote, nothing survives in the annotation, and every deletion carries a nil
// JSON value so the patch renders null.
func TestInvertDeletesEverythingItWrote(t *testing.T) {
	empty := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	provenance := record(empty)

	got, surviving := Invert(hardened(), provenance, Request{Requests: requests("10m", "32Mi")})

	if surviving != "" {
		t.Errorf("surviving annotation = %q, want empty: nothing was skipped", surviving)
	}
	if len(got.Changes) == 0 {
		t.Fatal("no deletions planned")
	}
	for _, c := range got.Changes {
		if c.JSON != nil {
			t.Errorf("%s: JSON = %v, want nil so the patch renders null", c.Path, c.JSON)
		}
	}
}

// AC-U03: the seccomp record deletes its parent, because seccompProfile.type
// is a required union discriminator and seccompProfile: {} fails validation.
// Every other record deletes its own leaf.
func TestInvertSeccompDeletesTheParent(t *testing.T) {
	got, _ := Invert(hardened(),
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault\n"+
			"spec.template.spec.containers[app].securityContext.capabilities.drop=[ALL]",
		Request{Requests: requests("10m", "32Mi")})

	want := []string{
		"spec.template.spec.containers[app].securityContext.capabilities.drop",
		"spec.template.spec.securityContext.seccompProfile",
	}
	if !slices.Equal(deletionPaths(got), want) {
		t.Errorf("deletion paths =\n  %s\nwant\n  %s",
			strings.Join(deletionPaths(got), "\n  "), strings.Join(want, "\n  "))
	}
}

// AC-U02 and Review Focus 1: a record whose live value a human changed is left
// alone, reported, and kept in the rewritten annotation. The [ALL] case must
// NOT report, because that is the comparison that silently breaks everything.
func TestInvertLeavesHumanEditsAlone(t *testing.T) {
	pod := hardened()
	pod.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("500m")

	provenance := "spec.template.spec.containers[app].resources.requests.cpu=10m\n" +
		"spec.template.spec.containers[app].securityContext.capabilities.drop=[ALL]"

	got, surviving := Invert(pod, provenance, Request{Requests: requests("10m", "32Mi")})

	if slices.Contains(deletionPaths(got), "spec.template.spec.containers[app].resources.requests.cpu") {
		t.Error("deleted a request a human had changed to 500m")
	}
	if !strings.Contains(reasons(got), "EditedSinceHardening") {
		t.Errorf("no EditedSinceHardening finding:\n%s", reasons(got))
	}
	if !strings.Contains(surviving, "resources.requests.cpu=10m") {
		t.Errorf("surviving = %q, want the edited record kept", surviving)
	}
	if !slices.Contains(deletionPaths(got), "spec.template.spec.containers[app].securityContext.capabilities.drop") {
		t.Error("capabilities.drop was not deleted; the [ALL] rendering does not match and everything silently stops")
	}
	if strings.Contains(surviving, "capabilities.drop") {
		t.Errorf("surviving = %q, want the reverted record dropped (BR-U05)", surviving)
	}
}

// AC-U04's unit half: in a restricted namespace the four always-on fields are
// skipped and reported while requests are still removed. readOnlyRootFilesystem
// is in neither standard and is always removable.
func TestInvertPodSecurityGate(t *testing.T) {
	empty := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	got, surviving := Invert(hardened(), record(empty), Request{
		Requests: requests("10m", "32Mi"),
		Enforce:  "restricted",
	})

	for _, blocked := range []string{"runAsNonRoot", "seccompProfile", "allowPrivilegeEscalation", "capabilities.drop"} {
		if slices.ContainsFunc(deletionPaths(got), func(p string) bool { return strings.Contains(p, blocked) }) {
			t.Errorf("%s was deleted under enforce: restricted", blocked)
		}
		if !strings.Contains(surviving, blocked) {
			t.Errorf("%s left the annotation though it was not removed", blocked)
		}
	}
	for _, removable := range []string{"resources.requests.cpu", "resources.requests.memory", "readOnlyRootFilesystem"} {
		if !slices.ContainsFunc(deletionPaths(got), func(p string) bool { return strings.Contains(p, removable) }) {
			t.Errorf("%s was not deleted; restricted does not require it", removable)
		}
	}
	if !strings.Contains(reasons(got), "BlockedByPodSecurity") {
		t.Errorf("no BlockedByPodSecurity finding:\n%s", reasons(got))
	}
}

// enforce: baseline requires none of the four — an absent seccompProfile is
// baseline-compliant — so baseline does not gate.
func TestInvertBaselineDoesNotGate(t *testing.T) {
	empty := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	got, surviving := Invert(hardened(), record(empty), Request{
		Requests: requests("10m", "32Mi"),
		Enforce:  "baseline",
	})
	if surviving != "" {
		t.Errorf("surviving = %q, want empty: baseline gates nothing", surviving)
	}
	if strings.Contains(reasons(got), "BlockedBy") {
		t.Errorf("baseline produced a block:\n%s", reasons(got))
	}
}

// AC-U05's unit half: a Container min with no default blocks removal of that
// request; a min for a resource something defaults does not, because the API
// server supplies the value.
func TestInvertLimitRangeGate(t *testing.T) {
	floor := []corev1.LimitRange{{
		ObjectMeta: meta("floor"),
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type: corev1.LimitTypeContainer,
			Min:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m")},
		}}},
	}}
	coverage, why := Cover(floor, requests("10m", "32Mi"))
	if why != "" {
		t.Fatalf("Cover refused the fixture: %s", why)
	}

	got, surviving := Invert(hardened(),
		"spec.template.spec.containers[app].resources.requests.cpu=10m\n"+
			"spec.template.spec.containers[app].resources.requests.memory=32Mi",
		Request{Requests: requests("10m", "32Mi"), Coverage: coverage})

	if slices.ContainsFunc(deletionPaths(got), func(p string) bool { return strings.HasSuffix(p, "requests.cpu") }) {
		t.Error("removed a cpu request a LimitRange min with no default requires")
	}
	if !slices.ContainsFunc(deletionPaths(got), func(p string) bool { return strings.HasSuffix(p, "requests.memory") }) {
		t.Error("memory has no min and should have been removed")
	}
	if !strings.Contains(reasons(got), "BlockedByLimitRange") {
		t.Errorf("no BlockedByLimitRange finding:\n%s", reasons(got))
	}
	if !strings.Contains(surviving, "requests.cpu=10m") {
		t.Errorf("surviving = %q, want the blocked record kept", surviving)
	}
}

// Error case: a recorded path already absent is dropped from the rewritten
// annotation and reported NoRecord — there is nothing to delete.
func TestInvertAbsentRecord(t *testing.T) {
	got, surviving := Invert(&corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		"spec.template.spec.containers[app].securityContext.capabilities.drop=[ALL]",
		Request{Requests: requests("10m", "32Mi")})

	if len(got.Changes) != 0 {
		t.Errorf("planned %v, want nothing: the field is already gone", deletionPaths(got))
	}
	if surviving != "" {
		t.Errorf("surviving = %q, want empty: an absent record is dropped", surviving)
	}
	if !strings.Contains(reasons(got), "NoRecord") {
		t.Errorf("no NoRecord finding:\n%s", reasons(got))
	}
}

// Review Focus 3: a malformed annotation is never guessed at. One finding, no
// deletions, and the annotation is left exactly as it was.
func TestInvertMalformedAnnotation(t *testing.T) {
	for _, bad := range []string{
		"this has no equals sign",
		"spec.template.spec.containers[app]",
		"=novalue",
	} {
		got, surviving := Invert(hardened(), bad, Request{Requests: requests("10m", "32Mi")})
		if len(got.Changes) != 0 {
			t.Errorf("%q: planned %v, want nothing", bad, deletionPaths(got))
		}
		if surviving != bad {
			t.Errorf("%q: surviving = %q, want the annotation untouched", bad, surviving)
		}
		if !strings.Contains(reasons(got), "Unparseable") {
			t.Errorf("%q: no Unparseable finding:\n%s", bad, reasons(got))
		}
	}
}

// Review Focus 3, the other half: a value containing an equals sign splits on
// the FIRST one. No recorded value contains one today, and a parser that
// splits on the last would be wrong the moment one does.
func TestInvertSplitsOnTheFirstEquals(t *testing.T) {
	got, _ := Invert(hardened(),
		"spec.template.spec.securityContext.seccompProfile.type=Runtime=Default",
		Request{Requests: requests("10m", "32Mi")})

	if len(got.Changes) != 0 {
		t.Errorf("planned %v; the recorded value Runtime=Default does not match the live RuntimeDefault", deletionPaths(got))
	}
	if !strings.Contains(reasons(got), "EditedSinceHardening") {
		t.Errorf("want EditedSinceHardening, got:\n%s", reasons(got))
	}
}

// Review Focus 4 / G-U01: a second WorkloadHardening replaced the first
// object's annotation, so records only the first wrote are simply gone. The
// undo reverts what the annotation says and claims nothing about the rest.
func TestInvertOnlyReadsTheAnnotation(t *testing.T) {
	got, surviving := Invert(hardened(),
		"spec.template.spec.containers[app].resources.requests.cpu=10m",
		Request{Requests: requests("10m", "32Mi")})

	if !slices.Equal(deletionPaths(got), []string{"spec.template.spec.containers[app].resources.requests.cpu"}) {
		t.Errorf("deletion paths = %v, want only the one recorded path", deletionPaths(got))
	}
	if surviving != "" {
		t.Errorf("surviving = %q, want empty", surviving)
	}
	// Every other field Build wrote is still on the template and untouched.
	if _, ok := ReadLeaf(hardened(), "spec.template.spec.securityContext.runAsNonRoot"); !ok {
		t.Error("the fixture lost a field the annotation never mentioned")
	}
}
```

Add a `meta(name string) metav1.ObjectMeta` helper if `resources_test.go` does not already have one — read it first and reuse whatever is there.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./pkg/plan/ -run TestInvert -v`
Expected: FAIL — `undefined: Invert`.

- [ ] **Step 4: Write the implementation**

Create `pkg/plan/invert.go`:

```go
package plan

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Finding reason prefixes (FR-U06). A prefix on the existing free-text reason
// rather than a new type: status renders one string per finding either way,
// and an operator greps for the word.
const (
	reasonEdited      = "EditedSinceHardening"
	reasonPodSecurity = "BlockedByPodSecurity"
	reasonLimitRange  = "BlockedByLimitRange"
	reasonNoRecord    = "NoRecord"
	reasonUnparseable = "Unparseable"
)

// restrictedFields are the four BR-02 always writes and the restricted Pod
// Security standard requires. Removing any of them from a template in a
// namespace labelled enforce: restricted makes every new pod inadmissible —
// and the workload patch itself succeeds, so the failure is a halted rollout
// rather than an error (BR-U04).
//
// readOnlyRootFilesystem is in neither standard, and resource requests are
// not Pod Security's business at all. Both are always removable.
var restrictedFields = []string{
	"securityContext.runAsNonRoot",
	"securityContext.seccompProfile.type",
	"securityContext.allowPrivilegeEscalation",
	"securityContext.capabilities.drop",
}

// Invert returns the deletions that undo one target's hardening, everything it
// declined to delete as findings, and the value the provenance annotation
// should carry afterwards — "" when nothing survives and the annotation is
// removed entirely (BR-U05).
//
// The second result is not in FR-U02's signature and has to be: BR-U05 rewrites
// the annotation in the same request as the deletions, Plan carries only
// Changes and Findings, and a Finding has no path. Deriving the survivors in
// the controller would mean reverse-mapping deletion paths back to records
// through BR-U03's table, which is this function's job and belongs here.
//
// Nothing here reads a cluster. The Enforce label and the LimitRange floor
// arrive on req, built once per namespace by the caller, so the decision stays
// pure and the reads stay in the controller.
func Invert(pod *corev1.PodSpec, provenance string, req Request) (Plan, string) {
	var p Plan

	records, err := parseProvenance(provenance)
	if err != nil {
		// Never guessed at. The annotation is the only evidence of what this
		// tool wrote, and half-reading it would delete the wrong fields.
		p.report("", "%s: the %s annotation cannot be read (%v); this target is left alone",
			reasonUnparseable, "hardening.acme.corp/filled", err)
		return p, provenance
	}

	var surviving []string
	for _, rec := range records {
		live, present := ReadLeaf(pod, rec.path)
		switch {
		case !present:
			// Already gone — a human removed it, or an earlier undo pass did.
			// Dropped from the annotation rather than kept: the record asserts
			// a field the workload does not have.
			p.report(containerOf(rec.path), "%s: %s is already absent; nothing to delete",
				reasonNoRecord, rec.path)
		case live != rec.value:
			// BR-01 read backwards. The annotation records values, not just
			// paths, precisely so a human's later edit is left alone.
			p.report(containerOf(rec.path), "%s: %s is now %q, not the %q this tool wrote; left alone",
				reasonEdited, rec.path, live, rec.value)
			surviving = append(surviving, rec.line())
		default:
			if why := gated(rec.path, req); why != "" {
				p.report(containerOf(rec.path), "%s", why)
				surviving = append(surviving, rec.line())
				continue
			}
			p.Changes = append(p.Changes, Change{
				Path: deletionPath(rec.path),
				// The recorded value, so status can render what is being
				// removed and the change hash moves when a human edits it.
				Value: rec.value,
				// nil marshals to null, which is how a strategic merge patch
				// deletes a key.
				JSON: nil,
			})
		}
	}

	// Sorted for the same reason Build sorts: the change hash is taken over
	// this list, and an unsorted plan would hash differently on most passes
	// and invalidate every approval (BR-U07).
	slices.SortFunc(p.Changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(surviving)
	return p, strings.Join(surviving, "\n")
}

// record is one path=value line of the provenance annotation.
type record struct{ path, value string }

func (r record) line() string { return r.path + "=" + r.value }

// parseProvenance reads the annotation. Splitting on the first "=" rather than
// the last: a path never contains one and a value might, so the first is the
// separator by construction.
func parseProvenance(annotation string) ([]record, error) {
	if strings.TrimSpace(annotation) == "" {
		return nil, nil
	}
	var out []record
	for i, line := range strings.Split(annotation, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path, value, found := strings.Cut(line, "=")
		if !found || path == "" {
			return nil, fmt.Errorf("line %d is not a path=value pair: %q", i+1, line)
		}
		out = append(out, record{path: path, value: value})
	}
	return out, nil
}

// deletionPath maps a record to the path actually sent as null (BR-U03).
//
// Normally the record's own leaf: deleting `resources` wholesale would take a
// limits block a human added after the apply, which is why the record is a
// leaf at all.
//
// One exception, and a table rather than a rule about required fields because
// there is exactly one entry. seccompProfile.type is a required union
// discriminator, so seccompProfile: {} fails API validation — and since a
// target yields exactly one strategic merge patch, that one field would take
// the whole target's undo down with it, requests included, every time.
// Deleting the parent is safe for the same reason the record exists: BR-01
// only filled the type because the effective value was absent, and an existing
// seccompProfile always carries a type, so the parent was created by this tool
// and holds nothing else.
func deletionPath(recorded string) string {
	if parent, ok := strings.CutSuffix(recorded, ".seccompProfile.type"); ok {
		return parent + ".seccompProfile"
	}
	return recorded
}

// gated returns the finding for a removal admission would refuse, or "".
//
// Both checks run against pods, not against the workload being patched, so
// neither shows in a dry-run: the patch succeeds, every new pod is rejected,
// and the rollout halts. A gated field is skipped, not fatal — the rest of
// this target's undo proceeds, because refusing the whole target would make
// the common case (hardening that broke a workload in a hardened namespace)
// unrecoverable by this tool (BR-U04).
func gated(recorded string, req Request) string {
	// Deliberately conservative: any `restricted` blocks all four, whatever
	// enforce-version pins. Refusing a removal that would have been legal
	// costs the operator a manual edit; the other error halts a rollout.
	if req.Enforce == "restricted" {
		for _, field := range restrictedFields {
			if strings.HasSuffix(recorded, field) {
				return fmt.Sprintf("%s: the namespace enforces the restricted standard, which requires %s; "+
					"removing it would make every new pod inadmissible and halt the rollout", reasonPodSecurity, field)
			}
		}
	}

	if name, ok := strings.CutPrefix(recorded[strings.LastIndex(recorded, "resources.requests.")+1:], "esources.requests."); ok &&
		strings.Contains(recorded, "resources.requests.") {
		if lr := req.Coverage.Floor(corev1.ResourceName(name)); lr != "" {
			return fmt.Sprintf("%s: LimitRange %q sets a Container min for %s with no default, "+
				"so a pod with no %s request is rejected at admission", reasonLimitRange, lr, name, name)
		}
	}
	return ""
}

// containerOf returns the container a recorded path names, or "" for a
// pod-level one, so a finding lands on the right row.
func containerOf(recorded string) string {
	rest, ok := strings.CutPrefix(recorded, templatePath+".")
	if !ok {
		return ""
	}
	_, key, isList := cutList(segment(rest))
	if !isList {
		return ""
	}
	return key
}
```

The `gated` resource extraction above is deliberately awkward to read; replace it with the straightforward version and keep the behaviour:

```go
	if i := strings.Index(recorded, "resources.requests."); i >= 0 {
		name := recorded[i+len("resources.requests."):]
		if lr := req.Coverage.Floor(corev1.ResourceName(name)); lr != "" {
			return fmt.Sprintf("%s: LimitRange %q sets a Container min for %s with no default, "+
				"so a pod with no %s request is rejected at admission", reasonLimitRange, lr, name, name)
		}
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/plan/ -run TestInvert -v`
Expected: all nine tests PASS.

- [ ] **Step 6: Verify 002's own tests still pass**

Run: `go test ./... -race && make cover`
Expected: every package `ok`, coverage at or above 90%. `Cover` grew a third pass and `Request` grew a field; neither changes what `Build` produces, so `pkg/plan`'s existing tests are the proof.

- [ ] **Step 7: Commit**

```bash
git add pkg/plan/invert.go pkg/plan/invert_test.go pkg/plan/plan.go pkg/plan/resources.go pkg/plan/resources_test.go
git commit -m "feat(plan): Invert, the deletion plan and the annotation that replaces it

The inverse of every change BR-01 makes is 'delete this field' — it only ever
fills an absent effective value, so there was never a previous one to restore.

Returns the rewritten annotation alongside the Plan, which FR-U02's signature
does not. BR-U05 rewrites it in the same request, Plan carries only Changes and
Findings, and a Finding has no path — so deriving the survivors in the
controller would mean reverse-mapping deletion paths back through BR-U03's
table, which is this function's job.

Three ways a record does not become a deletion, all kept in the annotation
except the first: already absent (NoRecord, dropped — the record asserts a
field the workload does not have), edited by a human (BR-01 read backwards),
or gated by admission. Neither gate shows in a dry-run, because both run
against pods and the object being patched is a workload: the patch succeeds
and the rollout halts.

Coverage gains Floor — the same LimitRanges BR-06 reads, asked the opposite
question: not 'is this request supplied' but 'is a missing one refused'.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: `Patch` takes its annotations (BR-U05, BR-U09, NFR-U02)

**Files:**
- Modify: `pkg/plan/patch.go`
- Modify: `pkg/plan/patch_test.go`
- Modify: `pkg/controller/execute.go` (002's only call site)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `plan.Patch(changes []Change, annotations map[string]any) ([]byte, error)` — errors on empty `changes`, as before.
  - `plan.MetadataPatch(annotations map[string]any) ([]byte, error)` — errors on empty `annotations`.
  - A `nil` annotation value renders as `null`, which removes the key.
  Tasks 6 and 7 call both.

`Patch` hardcodes `metadata.annotations[filled] = Provenance(changes)`, which is right for 002 and wrong in all three of an undo's cases: `filled` must carry the records that survived, or be removed with `null`, and `skip` and `skip-by` ride in the same document.

- [ ] **Step 1: Write the failing test**

Add to `pkg/plan/patch_test.go`:

```go
// The annotations are the caller's, and a nil value removes the key — which is
// how BR-U05 takes the provenance annotation off a fully reverted target in
// the same request as the deletions.
func TestPatchCarriesTheCallersAnnotations(t *testing.T) {
	body, err := Patch(
		[]Change{{Path: "spec.template.spec.containers[app].securityContext.capabilities.drop", JSON: nil}},
		map[string]any{
			"hardening.acme.corp/filled":  nil,
			"hardening.acme.corp/skip":    "true",
			"hardening.acme.corp/skip-by": "1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708",
		})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	got := string(body)
	for _, want := range []string{
		`"hardening.acme.corp/filled":null`,
		`"hardening.acme.corp/skip":"true"`,
		`"hardening.acme.corp/skip-by":"1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708"`,
		`"drop":null`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("patch body missing %s:\n%s", want, got)
		}
	}
}

// A metadata-only patch: the hold on a selected workload that carries no
// record (AC-U21). It must touch nothing under spec, or it would change the
// pod-template hash and start a rollout for two annotations.
func TestMetadataPatchTouchesNoSpec(t *testing.T) {
	body, err := MetadataPatch(map[string]any{
		"hardening.acme.corp/skip":    "true",
		"hardening.acme.corp/skip-by": "1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708",
	})
	if err != nil {
		t.Fatalf("MetadataPatch: %v", err)
	}
	if strings.Contains(string(body), `"spec"`) {
		t.Errorf("a hold patch reached spec; it would restart every pod:\n%s", body)
	}
	if _, err := MetadataPatch(nil); err == nil {
		t.Error("MetadataPatch(nil) returned no error; an empty patch is an API call that changes nothing")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/plan/ -run 'TestPatchCarries|TestMetadataPatch' -v`
Expected: FAIL — too many arguments to `Patch`, and `undefined: MetadataPatch`.

- [ ] **Step 3: Change the signature**

In `pkg/plan/patch.go`, replace `Patch` and add `MetadataPatch`:

```go
// Patch renders the strategic merge patch for changes, with the container
// lists keyed by name, and annotations in the same document — so a target is
// never patched without its record (BR-08, BR-U05, NFR-02).
//
// The annotations are the caller's rather than derived here. 002 passes the
// provenance of the changes it is writing; an undo passes the records that
// survived, or nil to remove the key, plus its hold (BR-U09). A value of nil
// renders as null, which is how a strategic merge patch deletes a key.
//
// A target with no gaps yields no patch and no API call, so an empty change
// set is still an error rather than an empty body: issuing it would restart
// every pod of the workload to write nothing. A caller that wants only
// annotations wants MetadataPatch.
func Patch(changes []Change, annotations map[string]any) ([]byte, error) {
	if len(changes) == 0 {
		return nil, errors.New("no changes: a target with no gaps yields no patch and no API call")
	}

	root := map[string]any{}
	for _, c := range changes {
		if err := insert(root, c.Path, c.JSON); err != nil {
			return nil, err
		}
	}
	setAnnotations(root, annotations)
	return json.Marshal(root)
}

// MetadataPatch renders a patch that sets annotations and nothing else: the
// hold on a selected workload with no records to revert (BR-U09, AC-U21).
//
// It reaches nothing under spec.template, so it changes no pod-template hash
// and starts no rollout — which is the whole reason a hold is not gated by
// approvedPlan the way a revert is.
func MetadataPatch(annotations map[string]any) ([]byte, error) {
	if len(annotations) == 0 {
		return nil, errors.New("no annotations: an empty patch is an API call that changes nothing")
	}
	root := map[string]any{}
	setAnnotations(root, annotations)
	return json.Marshal(root)
}

// setAnnotations writes the annotation keys directly rather than through
// insert's path walker: the keys carry dots and a slash.
func setAnnotations(root map[string]any, annotations map[string]any) {
	if len(annotations) == 0 {
		return
	}
	target := child(child(root, "metadata"), "annotations")
	for k, v := range annotations {
		target[k] = v
	}
}
```

- [ ] **Step 4: Update 002's call site**

In `pkg/controller/execute.go`, in `execute`:

```go
	body, err := plan.Patch(changes, map[string]any{v1alpha1.FilledAnnotation: plan.Provenance(changes)})
```

`Provenance` moves from inside `Patch` to the caller; its behaviour is unchanged.

- [ ] **Step 5: Run the tests**

Run: `go test ./pkg/plan/ -v && go test ./pkg/controller/ -race`
Expected: PASS. The existing `patch_test.go` cases that assert the provenance annotation appears will need their `Patch` calls updated to the two-argument form — that is a rename, not a behaviour change, and the assertions themselves must not move.

- [ ] **Step 6: Verify the provenance behaviour is byte-identical for 002**

Run: `go test ./pkg/controller/ -run 'TestApply|TestProvenance|TestPreview' -v`
Expected: PASS unedited. These assert the annotation 002 writes; if any needed a changed expectation, the refactor changed behaviour and must be reverted.

- [ ] **Step 7: Commit**

```bash
git add pkg/plan/patch.go pkg/plan/patch_test.go pkg/controller/execute.go
git commit -m "refactor(plan): Patch takes its annotations instead of deriving them

Patch hardcoded metadata.annotations[filled] = Provenance(changes), which is
right for 002 and wrong in all three of an undo's cases: filled carries the
records that survived, or is removed with null, and skip and skip-by ride in
the same document (BR-U05, BR-U09).

MetadataPatch alongside it, for the hold on a selected workload with nothing
to revert. It reaches nothing under spec.template, so it changes no
pod-template hash and starts no rollout — which is why a hold is not gated by
approvedPlan the way a revert is.

No behaviour change for 002: the call site passes what Patch used to compute.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: The API type and the CRD (FR-U01, FR-U06, AC-U12)

**Files:**
- Create: `pkg/apis/v1alpha1/undo.go`
- Create: `pkg/apis/v1alpha1/undo_test.go`
- Create: `deploy/crd-undo.yaml`
- Create: `hack/verify-crd-undo.sh`
- Modify: `pkg/apis/v1alpha1/hardening.go` (two `TargetStatus` fields, one new outcome)
- Modify: `deploy/crd-hardening.yaml` (the description)
- Modify: `Makefile`

**Interfaces:**
- Consumes: nothing.
- Produces, all used from Task 12 onwards:
  - `v1alpha1.UndoKind`, `UndoResource`, `UndoGroupVersionKind`, `UndoFinalizer`, `SkipByAnnotation`
  - `v1alpha1.UndoSpec{Namespaces []string; WorkloadSelector *metav1.LabelSelector; ApprovedPlan []string}`
  - `v1alpha1.UndoStatus{Phase; Message; Plan []TargetStatus; Findings []Finding; ApprovedCount; NamespaceCount; ObservedGeneration; LastReconcileTime}`
  - `v1alpha1.WorkloadHardeningUndo` with `Armed() bool`, `Approved(hash string) bool`
  - `UndoFromUnstructured`, `UndoToUnstructured`
  - `v1alpha1.OutcomeReverted`, `v1alpha1.OutcomeHeld`
  - `TargetStatus.Holder string`, `TargetStatus.HeldByMe bool`

- [ ] **Step 1: Extend the shared status type**

In `pkg/apis/v1alpha1/hardening.go`, add to the outcome constants:

```go
	// OutcomeReverted is an undo's Patched: the one place the direction of the
	// change is visible, and the one place a wrong word would mislead (FR-U06).
	OutcomeReverted = "Reverted"
	// OutcomeHeld is a selected workload with no records — held out of
	// hardening by a metadata-only patch, having reverted nothing.
	//
	// FR-U06 names no such outcome. AC-U21 requires the workload to be visibly
	// held, which needs a row, which needs an outcome; this is it.
	OutcomeHeld = "Held"
```

And to `TargetStatus`:

```go
	// Holder is the UID in this workload's hardening.acme.corp/skip-by, or "".
	// HeldByMe says whether it is this object's. Both are an undo's; 002 never
	// sets them, so they are omitempty and absent from its CRD (FR-U06).
	Holder   string `json:"holder,omitempty"`
	HeldByMe bool   `json:"heldByMe,omitempty"`
```

- [ ] **Step 2: Write the API type**

Create `pkg/apis/v1alpha1/undo.go`, mirroring `hardening.go` exactly in shape:

```go
package v1alpha1

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The WorkloadHardeningUndo API. It shares GroupName, Version, Phase and the
// per-target status types with WorkloadHardening, because the phase describes
// the disposition of the request rather than the direction of the change, and
// sharing the vocabulary shares the rendering code (FR-U06).
const (
	UndoKind = "WorkloadHardeningUndo"

	// SkipByAnnotation names the WorkloadHardeningUndo that wrote the skip
	// annotation beside it. skip is single-valued and shared, so without
	// knowing who set it the release would delete an exemption a human set by
	// hand — the precise harm BR-01 exists to prevent, committed by the
	// feature built to respect it (BR-U09).
	SkipByAnnotation = "hardening.acme.corp/skip-by"

	// UndoFinalizer holds the object open long enough to release its targets.
	//
	// Not the finalizer FR-05 refused. That one would have hung a delete on
	// work that can fail: undoing patches, halting rollouts, refusals from
	// admission. This one removes two metadata keys — it cannot be blocked by
	// Pod Security or a LimitRange, neither of which reads annotations, and it
	// starts no rollout (BR-U10).
	UndoFinalizer = "hardening.acme.corp/undo-release"
)

var UndoResource = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "workloadhardeningundos"}

var UndoGroupVersionKind = schema.GroupVersionKind{Group: GroupName, Version: Version, Kind: UndoKind}

// UndoSpec is immutable except for ApprovedPlan, enforced by a CEL transition
// rule in the CRD. workloadSelector is immutable with the rest: widening it
// after approval would change which workloads the approved hashes describe
// and, more to the point, which workloads the object claims (FR-U01, BR-U11).
type UndoSpec struct {
	Namespaces []string `json:"namespaces"`
	// WorkloadSelector matches the workload object's OWN metadata.labels, not
	// the pod template's — the same word meaning a different thing one level
	// down, and the mistake this field is most likely to invite. Absent
	// selects every workload in the named namespaces.
	WorkloadSelector *metav1.LabelSelector `json:"workloadSelector,omitempty"`
	ApprovedPlan     []string              `json:"approvedPlan,omitempty"`
}

// UndoStatus mirrors HardeningStatus. Written only when something other than
// the timestamp changed, so a resync of an unchanged object issues no writes.
type UndoStatus struct {
	Phase              Phase          `json:"phase,omitempty"`
	Message            string         `json:"message,omitempty"`
	Plan               []TargetStatus `json:"plan,omitempty"`
	Findings           []Finding      `json:"findings,omitempty"`
	ApprovedCount      int            `json:"approvedCount,omitempty"`
	NamespaceCount     int            `json:"namespaceCount,omitempty"`
	ObservedGeneration int64          `json:"observedGeneration,omitempty"`
	LastReconcileTime  string         `json:"lastReconcileTime,omitempty"`
}

type WorkloadHardeningUndo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UndoSpec   `json:"spec"`
	Status            UndoStatus `json:"status,omitempty"`
}

// Armed reports whether the operator has approved anything. An unarmed object
// previews its reverts and writes none of them — but it still holds its
// selected set, which is this feature's one divergence from "preview writes
// nothing" and the right order for an incident (BR-U09).
func (u *WorkloadHardeningUndo) Armed() bool { return len(u.Spec.ApprovedPlan) > 0 }

// Approved reports whether hash appears in spec.approvedPlan.
func (u *WorkloadHardeningUndo) Approved(hash string) bool {
	if hash == "" {
		return false
	}
	return slices.Contains(u.Spec.ApprovedPlan, hash)
}

func UndoFromUnstructured(o *unstructured.Unstructured) (*WorkloadHardeningUndo, error) {
	u := &WorkloadHardeningUndo{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, u); err != nil {
		return nil, err
	}
	return u, nil
}

func UndoToUnstructured(u *WorkloadHardeningUndo) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(u)
	if err != nil {
		return nil, err
	}
	o := &unstructured.Unstructured{Object: m}
	o.SetGroupVersionKind(UndoGroupVersionKind)
	return o, nil
}
```

- [ ] **Step 3: Write the unit test**

Create `pkg/apis/v1alpha1/undo_test.go` mirroring `hardening_test.go`: round-trip through unstructured with and without `workloadSelector` set, `Armed()` false on an empty and nil `approvedPlan`, `Approved("")` false, `Approved` exact-match only.

```go
// An absent workloadSelector must survive the round trip as absent, not as an
// empty selector. metav1.LabelSelector{} matches everything and so does nil,
// so the two agree today — but the CEL transition rule distinguishes them, and
// a converter that materialises one would make the field mutable exactly once.
func TestUndoRoundTripKeepsAnAbsentSelectorAbsent(t *testing.T) {
	u := &WorkloadHardeningUndo{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-rollback", Namespace: "isolation-system"},
		Spec:       UndoSpec{Namespaces: []string{"tenant-a"}},
	}
	o, err := UndoToUnstructured(u)
	if err != nil {
		t.Fatalf("UndoToUnstructured: %v", err)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(o.Object, "spec", "workloadSelector"); found {
		t.Error("an absent workloadSelector serialised as present")
	}
	back, err := UndoFromUnstructured(o)
	if err != nil {
		t.Fatalf("UndoFromUnstructured: %v", err)
	}
	if back.Spec.WorkloadSelector != nil {
		t.Errorf("workloadSelector = %+v, want nil", back.Spec.WorkloadSelector)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/apis/v1alpha1/ -v`
Expected: PASS.

- [ ] **Step 5: Write the CRD**

Create `deploy/crd-undo.yaml`, modelled on `deploy/crd-hardening.yaml`:

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: workloadhardeningundos.hardening.acme.corp
spec:
  group: hardening.acme.corp
  scope: Namespaced
  names:
    kind: WorkloadHardeningUndo
    listKind: WorkloadHardeningUndoList
    plural: workloadhardeningundos
    singular: workloadhardeningundo
    shortNames: [whu]
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      additionalPrinterColumns:
        - {name: Phase, type: string, jsonPath: .status.phase}
        - {name: Namespaces, type: integer, jsonPath: .status.namespaceCount}
        - {name: Approved, type: integer, jsonPath: .status.approvedCount}
        - {name: Age, type: date, jsonPath: .metadata.creationTimestamp}
      schema:
        openAPIV3Schema:
          type: object
          description: >-
            Removes the fields WorkloadHardening wrote, and holds the workloads
            it selects out of future hardening for as long as it exists.
            Creating the object holds its selected set and previews the revert;
            copying hashes out of status.plan into spec.approvedPlan performs
            it. Deleting the object releases the hold.
          required: [spec]
          properties:
            spec:
              type: object
              required: [namespaces]
              x-kubernetes-validations:
                # approvedPlan is the only mutable field. Unlike 002's rule,
                # this one needs has() guards: workloadSelector is optional, so
                # naming it unguarded raises a CEL runtime error on an object
                # that omits it — and a transition rule that errors rejects the
                # update, including the approvedPlan edit that arms it.
                - rule: >-
                    self.namespaces == oldSelf.namespaces &&
                    has(self.workloadSelector) == has(oldSelf.workloadSelector) &&
                    (!has(self.workloadSelector) ||
                     self.workloadSelector == oldSelf.workloadSelector)
                  message: >-
                    only spec.approvedPlan may be changed; delete this object
                    and create a new one to retarget
              properties:
                namespaces:
                  type: array
                  description: >-
                    1-16 namespaces to revert and hold, named explicitly.
                  minItems: 1
                  maxItems: 16
                  x-kubernetes-list-type: set
                  items:
                    type: string
                    maxLength: 63
                    pattern: "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
                workloadSelector:
                  type: object
                  description: >-
                    Matches the workload object's OWN metadata.labels, not the
                    pod template's. `kubectl get deploy --show-labels` is what
                    predicts it. Absent selects every workload in the named
                    namespaces.
                  required: [matchLabels]
                  properties:
                    matchLabels:
                      type: object
                      minProperties: 1
                      maxProperties: 8
                      additionalProperties:
                        type: string
                        maxLength: 63
                        pattern: "^$|^[a-z0-9A-Z]([-_.a-z0-9A-Z]*[a-z0-9A-Z])?$"
                  additionalProperties: false
                approvedPlan:
                  type: array
                  description: >-
                    Change hashes copied from status.plan. Empty means preview
                    only — the hold is still written, because it starts no
                    rollout and an incident wants the bypass first.
                  maxItems: 128
                  x-kubernetes-list-type: set
                  items:
                    type: string
                    pattern: "^[0-9a-f]{12}$"
            status:
              type: object
              x-kubernetes-preserve-unknown-fields: true
```

Copy the `status` schema from `deploy/crd-hardening.yaml` rather than using `preserve-unknown-fields` if that CRD declares one properly — read it and match. `matchExpressions` is deliberately not declared, and `additionalProperties: false` on `workloadSelector` is what rejects it (D-U06).

- [ ] **Step 6: Write the CRD verification script**

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

- [ ] **Step 7: Correct the hardening CRD description**

In `deploy/crd-hardening.yaml`, in the `openAPIV3Schema` description, replace `There is no undo.` with:

```
            Reverting is a separate kind, WorkloadHardeningUndo, which reads
            the provenance annotation this one writes and needs neither this
            object nor its status.
```

This is the commit where the old sentence becomes false, which is why Part A's docs pass (Task 6) leaves it alone — see Deviations item 5.

- [ ] **Step 8: Wire the CRD into deploy and verify-crd**

In the `Makefile`, add to `deploy`:

```make
	kubectl apply -f deploy/crd-undo.yaml
```

placed with the other two `kubectl apply -f deploy/crd*.yaml` lines. And:

```make
verify-crd-undo:
	./hack/verify-crd-undo.sh

verify-crd: verify-crd-isolation verify-crd-hardening verify-crd-undo
```

Add `verify-crd-undo` to `.PHONY`.

- [ ] **Step 9: Run it against a live cluster**

```bash
make kind-up || true
kubectl apply -f deploy/crd-undo.yaml
./hack/verify-crd-undo.sh
```

Expected: every case reports `ok`, and the script prints `AC-U12 PASSED`.

- [ ] **Step 10: Run the unit suite**

Run: `go test ./... -race && make cover`
Expected: every package `ok`, coverage at or above 90%.

- [ ] **Step 11: Commit**

```bash
git add pkg/apis/v1alpha1/undo.go pkg/apis/v1alpha1/undo_test.go pkg/apis/v1alpha1/hardening.go \
        deploy/crd-undo.yaml deploy/crd-hardening.yaml hack/verify-crd-undo.sh Makefile
git commit -m "feat(undo): the WorkloadHardeningUndo API and its CRD

A new kind rather than a mode on WorkloadHardening, because BR-U01's whole
argument is that an undo does not need the original object and must work after
it is deleted. A mode field would make the immutable-spec rule mean two things
and require the object to survive to be useful.

The transition rule is not 002's verbatim. 002 compares three fields with no
has() guard and its CRD says why: all three are always present after
defaulting. workloadSelector is optional, so an unguarded copy raises a CEL
runtime error on an object that omits it — and an erroring transition rule
rejects the update, including the approvedPlan edit that arms it. Four cases in
verify-crd-undo.sh exist for that.

Two additions to the shared TargetStatus for the holder, and one outcome the
spec does not name: Held, for a selected workload with no records. AC-U21
requires it to be visibly held, which needs a row, which needs an outcome.

crd-hardening.yaml stopped saying 'There is no undo.' in the commit that made
it false.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: Share discovery between the two features (BR-U06, FR-U01, AC-U08, AC-U19)

**Files:**
- Modify: `pkg/controller/targets.go`
- Modify: `pkg/controller/targets_test.go`
- Modify: `pkg/controller/hardening.go` (the one `discover` call)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `controller.workloadTarget` (was `hardeningTarget`) — the existing fields plus `Annotations map[string]string`, the workload object's own annotations. The undo reads `filled`, `skip` and `skip-by` off it, and hardening ignores it; carrying it here means the hold decision costs no second read.
  - `func discoverWorkloads(ctx context.Context, kube kubernetes.Interface, namespace string, opts discoverOpts) ([]workloadTarget, []v1alpha1.Finding, error)` — a free function, so both reconcilers call it.
  - `type discoverOpts struct { Selector labels.Selector; HonourSkip bool }` — a nil `Selector` matches everything.
  - `func ownedElsewhere(obj metav1.Object) string` — `excluded()`'s ownership half.
  Task 14 calls `discoverWorkloads` with `HonourSkip: false`.

002 passes `discoverOpts{HonourSkip: true}` and keeps today's behaviour exactly. The undo passes `HonourSkip: false`, which is BR-U06's first delta: a target carrying both `skip` and `filled` was hardened before someone asked to be left alone, and removing the hardening honours that intent rather than contradicting it.

- [ ] **Step 1: Write the failing test**

Add to `pkg/controller/targets_test.go`:

```go
// BR-U06's first delta, and AC-U07's last clause: an undo selects a
// skip-annotated workload, where hardening excludes it.
func TestDiscoverHonourSkipIsOptional(t *testing.T) {
	skipped := deployment("tenant-a", "opted-out", func(d *appsv1.Deployment) {
		d.Annotations = map[string]string{v1alpha1.SkipAnnotation: "true"}
	})
	kube := fake.NewClientset(ns("tenant-a"), deployment("tenant-a", "api"), skipped)

	hardening, _, err := discoverWorkloads(context.Background(), kube, "tenant-a", discoverOpts{HonourSkip: true})
	if err != nil {
		t.Fatalf("discoverWorkloads: %v", err)
	}
	if names(hardening) != "api" {
		t.Errorf("hardening targets = %q, want only api", names(hardening))
	}

	undo, _, err := discoverWorkloads(context.Background(), kube, "tenant-a", discoverOpts{HonourSkip: false})
	if err != nil {
		t.Fatalf("discoverWorkloads: %v", err)
	}
	if names(undo) != "api,opted-out" {
		t.Errorf("undo targets = %q, want both", names(undo))
	}
}

// AC-U19: the selector matches the workload object's own labels, not the pod
// template's. They are different fields one level apart and this is the
// mistake the field is most likely to invite.
func TestDiscoverSelectorMatchesTheWorkloadsOwnLabels(t *testing.T) {
	labelled := deployment("tenant-a", "api", func(d *appsv1.Deployment) {
		d.Labels = map[string]string{"app": "api"}
		d.Spec.Template.Labels = map[string]string{"app": "something-else"}
	})
	decoy := deployment("tenant-a", "worker", func(d *appsv1.Deployment) {
		d.Labels = map[string]string{"app": "worker"}
		// The pod template says api; the object does not. It must not match.
		d.Spec.Template.Labels = map[string]string{"app": "api"}
	})
	kube := fake.NewClientset(ns("tenant-a"), labelled, decoy)

	got, _, err := discoverWorkloads(context.Background(), kube, "tenant-a", discoverOpts{
		Selector: labels.SelectorFromSet(labels.Set{"app": "api"}),
	})
	if err != nil {
		t.Fatalf("discoverWorkloads: %v", err)
	}
	if names(got) != "api" {
		t.Errorf("targets = %q, want only api: the selector must read metadata.labels, not the pod template's", names(got))
	}
}

// An absent selector covers every workload in the namespace (FR-U01).
func TestDiscoverNilSelectorMatchesEverything(t *testing.T) {
	kube := fake.NewClientset(ns("tenant-a"), deployment("tenant-a", "api"), statefulSet("tenant-a", "db"))
	got, _, err := discoverWorkloads(context.Background(), kube, "tenant-a", discoverOpts{})
	if err != nil {
		t.Fatalf("discoverWorkloads: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("targets = %q, want both kinds", names(got))
	}
}

// names renders the target names in discovery order, comma separated.
func names(targets []workloadTarget) string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Ref.Name)
	}
	return strings.Join(out, ",")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/controller/ -run TestDiscover -v`
Expected: FAIL — `undefined: discoverWorkloads`.

- [ ] **Step 3: Convert discover to a free function with options**

In `pkg/controller/targets.go`:

1. Rename the type `hardeningTarget` to `workloadTarget` — it serves two features now:
   ```bash
   perl -pi -e 's/\bhardeningTarget\b/workloadTarget/g' $(grep -rl --include='*.go' 'hardeningTarget' pkg)
   ```
2. Add:
   ```go
   // discoverOpts is what differs between the two features' target sets. Every
   // other rule — the three kinds, the ownership exclusion, the paused /
   // OnDelete / partition refusals — is BR-04's and is shared unchanged.
   type discoverOpts struct {
   	// Selector filters on the workload object's OWN metadata.labels. Nil
   	// matches everything, which is 002's behaviour and an undo's default.
   	Selector labels.Selector
   	// HonourSkip excludes workloads annotated hardening.acme.corp/skip.
   	// True for hardening; false for an undo, because a target carrying both
   	// skip and filled was hardened before someone asked to be left alone, and
   	// removing the hardening honours that intent rather than contradicting
   	// it (BR-U06).
   	HonourSkip bool
   }

   // matches reports whether obj is in the selected set.
   func (o discoverOpts) matches(obj metav1.Object) bool {
   	return o.Selector == nil || o.Selector.Matches(labels.Set(obj.GetLabels()))
   }
   ```
3. Change the signature of `discover` to the free function `discoverWorkloads(ctx context.Context, kube kubernetes.Interface, namespace string, opts discoverOpts)`, replacing `r.Kube` with `kube` throughout and dropping the receiver. It calls `discoverFindings(ctx, kube, namespace)`, which becomes a free function the same way.
4. In each of the three workload loops, populate the new field when the target is appended:
   ```go
   		Annotations: d.Annotations,
   ```
5. In each of the three workload loops, immediately after the object is bound and before the exclusion check:
   ```go
   		if !opts.matches(d) {
   			continue
   		}
   ```
   Before the exclusion check, not after: a workload outside the selected set is not in scope at all, so it should not appear as a finding either.
6. Split `excluded`:
   ```go
   // ownedElsewhere reports why obj's template is someone else's to write, or
   // "". The ownership half of BR-04, shared by both features.
   func ownedElsewhere(obj metav1.Object) string {
   	if owner := controllerOf(obj); owner != nil {
   		return ownedBy(owner)
   	}
   	return ""
   }

   // excluded reports why obj must never be read as a hardening target, or "".
   // The skip annotation is checked first so an operator who set it sees their
   // own reason rather than an ownership one.
   func excluded(obj metav1.Object, honourSkip bool) string {
   	if honourSkip && obj.GetAnnotations()[v1alpha1.SkipAnnotation] == "true" {
   		return "annotated " + v1alpha1.SkipAnnotation + `="true": the escape hatch for a workload that needs what the policy would take away (BR-04)`
   	}
   	return ownedElsewhere(obj)
   }
   ```
   and pass `opts.HonourSkip` at the three call sites.
7. In `pkg/controller/hardening.go`, change the one call:
   ```go
   		targets, namespaceFindings, err := discoverWorkloads(ctx, r.Kube, namespace, discoverOpts{HonourSkip: true})
   ```

Add `"k8s.io/apimachinery/pkg/labels"` to the imports.

- [ ] **Step 4: Run the new tests and the whole suite**

Run: `go test ./pkg/controller/ -run TestDiscover -v && go test ./... -race && make cover`
Expected: PASS, every package `ok`, coverage at or above 90%.

- [ ] **Step 5: Verify 002's discovery behaviour is unchanged**

```bash
git diff -- pkg/controller/targets_test.go | grep '^-' | grep -v '^---'
```

Expected: only `hardeningTarget` → `workloadTarget` renames and the `discover(` → `discoverWorkloads(` call form. Any changed *assertion* means 002's target set moved, which this task must not do.

- [ ] **Step 6: Commit**

```bash
git add pkg/controller/targets.go pkg/controller/targets_test.go pkg/controller/hardening.go
git commit -m "refactor(controller): one discovery for both features

The undo needs the same three kinds, the same ownership exclusion and the same
paused / OnDelete / partition refusals, and differs in exactly two ways: it
filters on the workload object's own labels, and it does not honour the skip
annotation. Two options rather than a second hundred-line copy.

hardeningTarget becomes workloadTarget; discover becomes a free function, so
the undo reconciler can call it without borrowing the hardening one's
receiver.

The selector filter runs before the exclusion check: a workload outside the
selected set is not in scope at all, so it should not surface as a finding
either.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 13: The hold, the release and exclusivity, as pure functions (BR-U09, BR-U10, BR-U11, AC-U20)

**Files:**
- Create: `pkg/controller/hold.go`
- Create: `pkg/controller/hold_test.go`

**Interfaces:**
- Consumes: `v1alpha1.SkipAnnotation`, `v1alpha1.SkipByAnnotation` from Task 11.
- Produces, all pure and all called from Task 14:
  - `func holdFor(annotations map[string]string, me string) map[string]any` — the annotations to write to claim one target, or nil.
  - `func releaseFor(annotations map[string]string, me string) map[string]any` — the annotations to remove, or nil.
  - `func holder(annotations map[string]string) (uid string, humanSet bool)`
  - `func firstConflict(targets []workloadTarget, held map[string]map[string]string, me string, live map[string]bool) (target, holder string, found bool)`

- [ ] **Step 1: Write the failing test**

Create `pkg/controller/hold_test.go`:

```go
package controller

import (
	"testing"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

const (
	me    = "1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708"
	other = "99887766-5544-3322-1100-aabbccddeeff"
)

func ann(pairs ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

// BR-U09's write table, all four cases.
func TestHoldFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current map[string]string
		want    map[string]any
	}{{
		name:    "no skip: claim it",
		current: nil,
		want:    map[string]any{v1alpha1.SkipAnnotation: "true", v1alpha1.SkipByAnnotation: me},
	}, {
		name:    "already mine: the steady state, nothing to write",
		current: ann(v1alpha1.SkipAnnotation, "true", v1alpha1.SkipByAnnotation, me),
		want:    nil,
	}, {
		name:    "another object's: BR-U11 rejected this object already",
		current: ann(v1alpha1.SkipAnnotation, "true", v1alpha1.SkipByAnnotation, other),
		want:    nil,
	}, {
		name:    "a human's bare skip: not ours to mark",
		current: ann(v1alpha1.SkipAnnotation, "true"),
		want:    nil,
	}, {
		name:    "my marker with the skip stripped by hand: re-assert it",
		current: ann(v1alpha1.SkipByAnnotation, me),
		want:    map[string]any{v1alpha1.SkipAnnotation: "true", v1alpha1.SkipByAnnotation: me},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := holdFor(tc.current, me)
			if len(got) != len(tc.want) {
				t.Fatalf("holdFor = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("holdFor[%s] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

// AC-U15 and AC-U20: the release takes back only what this object wrote. A
// human's bare skip survives an undo's whole lifecycle.
func TestReleaseFor(t *testing.T) {
	mine := releaseFor(ann(v1alpha1.SkipAnnotation, "true", v1alpha1.SkipByAnnotation, me), me)
	if mine[v1alpha1.SkipAnnotation] != nil || mine[v1alpha1.SkipByAnnotation] != nil {
		t.Errorf("releaseFor(mine) = %v, want both keys set to nil", mine)
	}
	if len(mine) != 2 {
		t.Errorf("releaseFor(mine) = %v, want exactly the two keys", mine)
	}

	for _, current := range []map[string]string{
		ann(v1alpha1.SkipAnnotation, "true"),                                  // a human's
		ann(v1alpha1.SkipAnnotation, "true", v1alpha1.SkipByAnnotation, other), // someone else's
		nil,
	} {
		if got := releaseFor(current, me); got != nil {
			t.Errorf("releaseFor(%v) = %v, want nil: touch only what you wrote", current, got)
		}
	}
}

// AC-U18 and Review Focus 5: a marker naming a live object blocks; one naming
// a UID with no live object is not a claim and is taken over.
func TestFirstConflict(t *testing.T) {
	targets := []workloadTarget{
		{Ref: target("tenant-a", "Deployment", "api")},
		{Ref: target("tenant-a", "Deployment", "worker")},
	}
	held := map[string]map[string]string{
		"Deployment/tenant-a/worker": ann(v1alpha1.SkipAnnotation, "true", v1alpha1.SkipByAnnotation, other),
	}

	name, by, found := firstConflict(targets, held, me, map[string]bool{other: true})
	if !found || by != other {
		t.Fatalf("firstConflict = (%q, %q, %v), want the worker held by %s", name, by, found, other)
	}
	if name != "Deployment/tenant-a/worker" {
		t.Errorf("conflict names %q, want the held workload", name)
	}

	// The same annotation, with no live object behind it.
	if _, _, found := firstConflict(targets, held, me, map[string]bool{}); found {
		t.Error("an orphaned skip-by blocked a new undo; it would block every future one and the only remedy would be editing it by hand")
	}

	// My own marker never conflicts with me.
	mine := map[string]map[string]string{
		"Deployment/tenant-a/api": ann(v1alpha1.SkipAnnotation, "true", v1alpha1.SkipByAnnotation, me),
	}
	if _, _, found := firstConflict(targets, mine, me, map[string]bool{me: true}); found {
		t.Error("an object conflicted with its own hold")
	}
}
```

Add a `target(namespace, kind, name string) plan.Target` helper if one is not already in the package.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/controller/ -run 'TestHoldFor|TestReleaseFor|TestFirstConflict' -v`
Expected: FAIL — `undefined: holdFor`.

- [ ] **Step 3: Write the implementation**

Create `pkg/controller/hold.go`:

```go
package controller

import (
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// holder reports the UID claiming a workload and whether the skip annotation
// is a human's. A skip with no skip-by is a hand-set exemption: not ours to
// mark, and not ours to remove (BR-U09, BR-U10).
func holder(annotations map[string]string) (uid string, humanSet bool) {
	uid = annotations[v1alpha1.SkipByAnnotation]
	return uid, uid == "" && annotations[v1alpha1.SkipAnnotation] == "true"
}

// holdFor returns the annotations to write to hold one target, or nil when
// there is nothing to write.
//
// The marker is not bookkeeping for its own sake. skip is single-valued and
// shared: without knowing who set it, the release would delete an exemption a
// human set by hand — the precise harm BR-01 exists to prevent, committed by
// the feature built to respect it. With it, the rule is BR-U02's discipline
// applied to one more field: touch only what you wrote, and only if it is
// unchanged.
func holdFor(annotations map[string]string, me string) map[string]any {
	uid, humanSet := holder(annotations)
	switch {
	case humanSet:
		// A human's exemption — including one whose skip-by someone stripped,
		// which is indistinguishable from it and is left alone for the same
		// reason.
		return nil
	case uid != "" && uid != me:
		// Another object's. BR-U11 rejected this object before it planned
		// anything, so this branch is a belt-and-braces guard rather than a
		// path a correct pass reaches.
		return nil
	case uid == me && annotations[v1alpha1.SkipAnnotation] == "true":
		// Already held. The steady state, and what makes a resync of an intact
		// object issue no writes at all (AC-U22).
		return nil
	default:
		// Unheld, or my own marker with the skip stripped by hand — which is
		// exactly the lapse the re-assert exists to repair (BR-U09).
		return map[string]any{
			v1alpha1.SkipAnnotation:   "true",
			v1alpha1.SkipByAnnotation: me,
		}
	}
}

// releaseFor returns the annotations to remove when this object is deleted, or
// nil. Both keys are set to nil, which is how a strategic merge patch deletes
// them (BR-U10).
func releaseFor(annotations map[string]string, me string) map[string]any {
	if uid, _ := holder(annotations); uid != me {
		return nil
	}
	return map[string]any{
		v1alpha1.SkipAnnotation:   nil,
		v1alpha1.SkipByAnnotation: nil,
	}
}

// firstConflict returns the first target in the selected set already claimed
// by a different, live undo object (BR-U11).
//
// held maps a target's String() to its annotations; live is the set of UIDs
// with a WorkloadHardeningUndo behind them.
//
// A skip-by whose UID matches no live object is not a claim. A finalizer
// force-cleared, or an object removed while the controller was down, would
// otherwise leave one annotation blocking every future bypass of that workload
// with no remedy but editing it by hand.
func firstConflict(
	targets []workloadTarget,
	held map[string]map[string]string,
	me string,
	live map[string]bool,
) (target, by string, found bool) {
	for _, t := range targets {
		uid, _ := holder(held[t.Ref.String()])
		if uid == "" || uid == me || !live[uid] {
			continue
		}
		return t.Ref.String(), uid, true
	}
	return "", "", false
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/controller/ -run 'TestHoldFor|TestReleaseFor|TestFirstConflict' -v`
Expected: all three PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/controller/hold.go pkg/controller/hold_test.go
git commit -m "feat(undo): the hold, the release and the exclusivity check, as pure functions

Four decisions, no clients: what to write onto a target to claim it, what to
take back when the object is deleted, who holds a workload, and which target
in a selected set is already someone else's.

skip is single-valued and shared, so skip-by is what makes the release safe:
without it, releasing would delete an exemption a human set by hand — the
precise harm BR-01 exists to prevent, committed by the feature built to
respect it. A skip with no skip-by is therefore never marked and never
removed, and neither is one whose marker someone stripped.

A skip-by naming a UID with no live object is not a claim. A force-cleared
finalizer would otherwise leave one annotation blocking every future bypass of
that workload, with no remedy but editing it by hand.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 14: Undo fixtures, validation and exclusivity (FR-U05, BR-U04, BR-U11, AC-U07, AC-U18, AC-U19)

**Files:**
- Create: `pkg/controller/undo.go`
- Create: `pkg/controller/undo_test.go`

**Interfaces:**
- Consumes: the API types (Task 11), `discoverWorkloads` (Task 12), `firstConflict` (Task 13).
- Produces:
  - `controller.UndoReconciler{Kube kubernetes.Interface; Dyn dynamic.Interface; Protected map[string]bool; Timeout time.Duration; Now func() time.Time}` with `Reconcile(ctx context.Context, key string) error`.
  - `(*UndoReconciler).validate(ctx, u) (map[string]plan.Request, error)` — one `plan.Request` per namespace, carrying `Coverage` from its LimitRanges and `Enforce` from its `pod-security.kubernetes.io/enforce` label.
  - `(*UndoReconciler).setStatus(ctx, u, want v1alpha1.UndoStatus) error`.
  - The test fixtures Tasks 8 and 9 build on: `newUndoer`, `undo`, `undoDynClient`, `storedUndo`, `undoKey`, `hardenedDeployment`.

Read `pkg/controller/hardening.go` before writing `undo.go`. `Reconcile`, `validate`, `setStatus`, `reportFailure` and `namespaces` all have direct counterparts there; follow their structure and their comments, and write only the differences this plan names.

- [ ] **Step 1: Write the fixtures and the first failing tests**

Create `pkg/controller/undo_test.go`:

```go
package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"
)

// undoUID is this feature's request UID, distinct from 001's uid and 002's
// hardUID, both already declared in this package.
const undoUID = "7f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4051"

// undo builds an unarmed undo over the given namespaces, with the finalizer
// already on it — the controller persists it before the first hold, so every
// test past the first pass sees it there.
func undo(namespaces ...string) *v1alpha1.WorkloadHardeningUndo {
	return &v1alpha1.WorkloadHardeningUndo{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tenant-rollback",
			Namespace:  "isolation-system",
			UID:        undoUID,
			Finalizers: []string{v1alpha1.UndoFinalizer},
		},
		Spec: v1alpha1.UndoSpec{Namespaces: namespaces},
	}
}

func newUndoer(objects ...runtime.Object) *UndoReconciler {
	return &UndoReconciler{
		Kube: fake.NewClientset(objects...),
		Protected: map[string]bool{
			"kube-system": true, "kube-public": true, "kube-node-lease": true,
			"isolation-system": true,
		},
		Timeout: 5 * time.Second,
		Now:     func() time.Time { return time.Unix(1700000000, 0) },
	}
}

// undoDynClient holds one or more undo objects. The custom list kinds are
// required: the fake cannot infer one for an unregistered CRD.
func undoDynClient(t *testing.T, undos ...*v1alpha1.WorkloadHardeningUndo) *dynamicfake.FakeDynamicClient {
	t.Helper()
	var objects []runtime.Object
	for _, u := range undos {
		o, err := v1alpha1.UndoToUnstructured(u)
		if err != nil {
			t.Fatalf("UndoToUnstructured: %v", err)
		}
		objects = append(objects, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			v1alpha1.Resource:          v1alpha1.Kind + "List",
			v1alpha1.HardeningResource: v1alpha1.HardeningKind + "List",
			v1alpha1.UndoResource:      v1alpha1.UndoKind + "List",
		},
		objects...,
	)
}

func storedUndo(t *testing.T, r *UndoReconciler, u *v1alpha1.WorkloadHardeningUndo) *v1alpha1.WorkloadHardeningUndo {
	t.Helper()
	o, err := r.Dyn.Resource(v1alpha1.UndoResource).Namespace(u.Namespace).Get(context.Background(), u.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the undo back: %v", err)
	}
	got, err := v1alpha1.UndoFromUnstructured(o)
	if err != nil {
		t.Fatalf("UndoFromUnstructured: %v", err)
	}
	return got
}

func undoKey(u *v1alpha1.WorkloadHardeningUndo) string { return u.Namespace + "/" + u.Name }

// undoRequests is the policy 002 was applied with, and therefore the values
// recorded in every fixture's provenance annotation.
func undoRequests() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	}
}

// hardenedDeployment is a Deployment as 002 leaves it: every always-on field
// written into the template, requests filled, and a provenance annotation
// recording exactly that.
//
// Hand-written rather than derived, so a reader can see what is being reverted
// — and pinned against Build by TestHardenedFixtureMatchesWhatBuildWrites
// below, so it cannot drift into testing a template 002 would never produce.
func hardenedDeployment(namespace, name string, mutate ...func(*appsv1.Deployment)) *appsv1.Deployment {
	yes, no := true, false
	d := deployment(namespace, name)
	d.Annotations = map[string]string{v1alpha1.FilledAnnotation: strings.Join([]string{
		"spec.template.spec.containers[app].resources.requests.cpu=10m",
		"spec.template.spec.containers[app].resources.requests.memory=32Mi",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation=false",
		"spec.template.spec.containers[app].securityContext.capabilities.drop=[ALL]",
		"spec.template.spec.securityContext.runAsNonRoot=true",
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault",
	}, "\n")}
	d.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot:   &yes,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	d.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: &no,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	d.Spec.Template.Spec.Containers[0].Resources.Requests = undoRequests()
	for _, m := range mutate {
		m(d)
	}
	return d
}

// The fixture is only useful if it is what 002 actually produces. Build the
// same bare template through Build and assert the annotation matches
// line for line — otherwise every test below reverts something imaginary.
func TestHardenedFixtureMatchesWhatBuildWrites(t *testing.T) {
	bare := deployment("tenant-a", "api")
	built := plan.Build(&bare.Spec.Template.Spec, plan.Request{Requests: undoRequests()})

	want := plan.Provenance(built.Changes)
	got := hardenedDeployment("tenant-a", "api").Annotations[v1alpha1.FilledAnnotation]
	if got != want {
		t.Errorf("the hardened fixture's annotation has drifted from Build:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// AC-U07, first half: a protected namespace rejects the whole object, before
// any target is read and before any hold is written.
func TestUndoRejectsAProtectedNamespace(t *testing.T) {
	u := undo("isolation-system")
	r := newUndoer(ns("isolation-system"), hardenedDeployment("isolation-system", "api"))
	r.Dyn = undoDynClient(t, u)

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := storedUndo(t, r, u)
	if got.Status.Phase != v1alpha1.PhaseRejected {
		t.Errorf("phase = %q, want Rejected", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "protected") {
		t.Errorf("message = %q, want it to name the protected namespace", got.Status.Message)
	}
	if d := storedTemplate(t, r.Kube, "isolation-system", "api"); d.Annotations[v1alpha1.SkipAnnotation] != "" {
		t.Error("a rejected object wrote a hold; validation refuses before anything is claimed")
	}
}

// AC-U07, second half: paused, OnDelete and partition > 0 are refused with the
// remedy named. An undo patch sits just as inert on a workload that will not
// roll out.
func TestUndoRefusesTargetsThatWillNotRoll(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(
		ns("tenant-a"),
		hardenedDeployment("tenant-a", "paused", func(d *appsv1.Deployment) { d.Spec.Paused = true }),
		statefulSet("tenant-a", "ondelete", func(s *appsv1.StatefulSet) {
			s.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
		}),
	)
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := storedUndo(t, r, u)
	var reasons string
	for _, f := range got.Status.Findings {
		reasons += f.Name + ": " + f.Reason + "\n"
	}
	for _, want := range []string{"paused", "OnDelete"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("no finding naming %s:\n%s", want, reasons)
		}
	}
	for _, row := range got.Status.Plan {
		if row.Name == "paused" || row.Name == "ondelete" {
			t.Errorf("%s produced a plan row; a refused target is a finding, not a target", row.Name)
		}
	}
}

// AC-U18: an undo whose selected set includes a workload another live object
// holds is Rejected naming both, and reaches Previewed on the resync after
// that object is deleted. A skip-by naming no live object does not block.
func TestUndoExclusivity(t *testing.T) {
	const otherUID = "99887766-5544-3322-1100-aabbccddeeff"
	claimed := hardenedDeployment("tenant-a", "api", func(d *appsv1.Deployment) {
		d.Annotations[v1alpha1.SkipAnnotation] = "true"
		d.Annotations[v1alpha1.SkipByAnnotation] = otherUID
	})

	mine := undo("tenant-a")
	theirs := undo("tenant-a")
	theirs.Name = "other-rollback"
	theirs.UID = otherUID

	r := newUndoer(ns("tenant-a"), claimed)
	r.Dyn = undoDynClient(t, mine, theirs)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(mine)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := storedUndo(t, r, mine)
	if got.Status.Phase != v1alpha1.PhaseRejected {
		t.Fatalf("phase = %q (%s), want Rejected", got.Status.Phase, got.Status.Message)
	}
	for _, want := range []string{"api", "other-rollback"} {
		if !strings.Contains(got.Status.Message, want) {
			t.Errorf("message = %q, want it to name %s", got.Status.Message, want)
		}
	}

	// Delete the holder. Rejected is non-terminal, so the next resync proceeds
	// and takes over the orphaned marker (Review Focus 5).
	if err := r.Dyn.Resource(v1alpha1.UndoResource).Namespace(theirs.Namespace).
		Delete(context.Background(), theirs.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting the other undo: %v", err)
	}
	if err := r.Reconcile(context.Background(), undoKey(mine)); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := storedUndo(t, r, mine); got.Status.Phase == v1alpha1.PhaseRejected {
		t.Errorf("still Rejected (%s); an orphaned skip-by is not a claim", got.Status.Message)
	}
}

// Two undos over one namespace with disjoint selectors both proceed (AC-U19).
// A bypass names workloads, so unlike 001's per-namespace refusal there is
// nothing namespace-wide to collide over.
func TestTwoUndosWithDisjointSelectorsBothProceed(t *testing.T) {
	api := hardenedDeployment("tenant-a", "api", func(d *appsv1.Deployment) {
		d.Labels = map[string]string{"app": "api"}
	})
	worker := hardenedDeployment("tenant-a", "worker", func(d *appsv1.Deployment) {
		d.Labels = map[string]string{"app": "worker"}
	})

	first := undo("tenant-a")
	first.Spec.WorkloadSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	second := undo("tenant-a")
	second.Name = "worker-rollback"
	second.UID = "11111111-2222-3333-4444-555555555555"
	second.Spec.WorkloadSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "worker"}}

	r := newUndoer(ns("tenant-a"), api, worker)
	r.Dyn = undoDynClient(t, first, second)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	for _, u := range []*v1alpha1.WorkloadHardeningUndo{first, second} {
		if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
			t.Fatalf("Reconcile(%s): %v", u.Name, err)
		}
		if got := storedUndo(t, r, u); got.Status.Phase == v1alpha1.PhaseRejected {
			t.Errorf("%s = Rejected (%s); the selectors are disjoint", u.Name, got.Status.Message)
		}
	}

	// And each claimed only its own.
	if got := storedTemplate(t, r.Kube, "tenant-a", "api"); got.Annotations[v1alpha1.SkipByAnnotation] != string(first.UID) {
		t.Errorf("api held by %q, want the api-selecting object", got.Annotations[v1alpha1.SkipByAnnotation])
	}
	if got := storedTemplate(t, r.Kube, "tenant-a", "worker"); got.Annotations[v1alpha1.SkipByAnnotation] != string(second.UID) {
		t.Errorf("worker held by %q, want the worker-selecting object", got.Annotations[v1alpha1.SkipByAnnotation])
	}
}
```

`storedTemplate` and `patchActions` take a `*HardeningReconciler` today. Change both to take a `kubernetes.Interface` and update the four call sites in `hardening_test.go`. That is a test-only signature change and no assertion moves.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./pkg/controller/ -run 'TestHardenedFixture|TestUndo|TestTwoUndos' -v`
Expected: FAIL — `undefined: UndoReconciler`.

- [ ] **Step 3: Write the reconciler skeleton**

Create `pkg/controller/undo.go` with `UndoReconciler`, `Reconcile`, `validate`, `setStatus`, `reportFailure` and `undoNamespaces`, following `hardening.go`. Three differences from its counterpart, and only these:

1. **`validate` reads the `enforce` label as well as the LimitRanges**, and has no requests policy to check:
   ```go
   		limitRanges, err := r.Kube.CoreV1().LimitRanges(namespace).List(ctx, metav1.ListOptions{})
   		if err != nil {
   			return nil, err
   		}
   		// The requests this tool would have written, which is what Cover needs
   		// to decide which bounds apply. An undo has no policy of its own: what
   		// to remove is on the workloads. The values only have to be present
   		// for Floor to be computed over the right resource names.
   		coverage, _ := plan.Cover(limitRanges.Items, undoCoveredResources)
   		requests[namespace] = plan.Request{
   			Coverage: coverage,
   			// Read from the namespace object already fetched above. Any
   			// `restricted` blocks all four always-on fields, whatever
   			// enforce-version pins — refusing a legal removal costs a manual
   			// edit; the other error halts a rollout (BR-U04).
   			Enforce: nsObj.Labels["pod-security.kubernetes.io/enforce"],
   		}
   ```
   `Cover`'s second result is discarded deliberately: it refuses values this tool would *write*, and an undo writes none. Comment that.
   `undoCoveredResources` is `corev1.ResourceList{corev1.ResourceCPU: {}, corev1.ResourceMemory: {}}` — the two resources 002 can fill, so the two `Floor` can be asked about.

2. **The exclusivity check**, after discovery and before anything is planned or written:
   ```go
   	// BR-U11, before the first hold: a bypass is a standing claim, and two
   	// standing claims over one workload cannot both be honoured by a
   	// single-valued annotation. Rejected is non-terminal, so the object
   	// starts working by itself once the conflicting rule is deleted.
   	if name, by, found := firstConflict(targets, held, string(u.UID), live); found {
   		return r.setStatus(ctx, u, v1alpha1.UndoStatus{
   			Phase: v1alpha1.PhaseRejected,
   			Message: fmt.Sprintf("workload %s is already held by WorkloadHardeningUndo %s; "+
   				"narrow spec.workloadSelector or delete that object", name, by),
   		})
   	}
   ```
   `live` comes from one cluster-wide `List` of `v1alpha1.UndoResource`, keyed by UID. `held` maps `target.Ref.String()` to that workload's annotations, built from the discovered targets — so it costs no extra read.
   The message names the holding object by **name**, not by UID: look it up in the same list, because a UID is not something an operator can `kubectl delete`.

3. **`Reconcile` has a deletion branch** on `u.DeletionTimestamp != nil`, which Task 16 fills in. For now, return `nil` from it with a `TODO` that Task 16 replaces — and note it here so it is not mistaken for finished work.

- [ ] **Step 4: Run the tests until green**

Run: `go test ./pkg/controller/ -run 'TestHardenedFixture|TestUndo|TestTwoUndos' -v`
Expected: all five PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go test ./... -race`
Expected: every package `ok`. Coverage may dip below 90% here because `undo.go` has paths no test reaches yet; Tasks 8 and 9 close it, and `make cover` is checked at the end of Task 16.

- [ ] **Step 6: Commit**

```bash
git add pkg/controller/undo.go pkg/controller/undo_test.go pkg/controller/hardening_test.go
git commit -m "feat(undo): the reconciler's skeleton — validation and exclusivity

Mirrors hardening.go's Reconcile/validate/setStatus, with three differences:
validate reads the namespace's pod-security enforce label beside its
LimitRanges, there is no requests policy to check because what to remove is on
the workloads, and BR-U11 runs after discovery and before anything is written.

The exclusivity check costs one cluster-wide list of undo objects and no extra
workload read: the annotations come from the targets discovery already
returned. It names the holder by name rather than by UID, because a UID is not
something an operator can delete.

The fixture is pinned against Build: a hardened Deployment whose provenance
annotation drifts from what 002 actually writes would make every test below
revert something imaginary.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 15: Preview and apply the revert (FR-U03, FR-U04, BR-U07, AC-U01, AC-U06, AC-U09, AC-U10)

**Files:**
- Modify: `pkg/controller/undo.go`
- Modify: `pkg/controller/undo_test.go`

**Interfaces:**
- Consumes: `plan.Invert` (Task 9), `plan.Patch` (Task 10), the dry-run cache in `execute.go`.
- Produces: `(*UndoReconciler).execute(ctx, logger, u, t workloadTarget, p plan.Plan, surviving string, row v1alpha1.TargetStatus) v1alpha1.TargetStatus` and `undoPhaseFor(u, rows) (v1alpha1.Phase, string)`. Task 16 adds the hold to the same patch.

Reuse `execute.go`'s `dryRun()`, `cached`/`remember`/`forget`/`retain` and `approvedEarlier` unchanged — they are keyed by request UID and target, which is as true of an undo as of a hardening. Generalise their parameter from `*v1alpha1.WorkloadHardening` to an interface with `Armed()`, `Approved(string)`, plus the namespace/name/UID from `metav1.Object`; the bodies do not change.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/controller/undo_test.go`:

```go
// arm returns a copy of u with every hash in the stored plan approved.
func arm(t *testing.T, r *UndoReconciler, u *v1alpha1.WorkloadHardeningUndo) *v1alpha1.WorkloadHardeningUndo {
	t.Helper()
	previewed := storedUndo(t, r, u)
	armed := previewed.DeepCopy()
	for _, row := range previewed.Status.Plan {
		if row.Hash != "" {
			armed.Spec.ApprovedPlan = append(armed.Spec.ApprovedPlan, row.Hash)
		}
	}
	armed.Generation = previewed.Generation + 1
	r.Dyn = undoDynClient(t, armed)
	return armed
}

// AC-U01: a workload hardened by 002 and then undone retains no field this
// tool wrote, and its filled annotation is gone — in the same patch.
func TestUndoRevertsEverythingItWrote(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), hardenedDeployment("tenant-a", "api"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	armed := arm(t, r, u)
	if err := r.Reconcile(context.Background(), undoKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	d := storedTemplate(t, r.Kube, "tenant-a", "api")
	pod := &d.Spec.Template.Spec
	if pod.SecurityContext != nil && pod.SecurityContext.RunAsNonRoot != nil {
		t.Error("runAsNonRoot survived the undo")
	}
	if pod.SecurityContext != nil && pod.SecurityContext.SeccompProfile != nil {
		t.Error("seccompProfile survived; BR-U03 deletes the parent, not the type")
	}
	c := pod.Containers[0]
	if c.SecurityContext != nil && c.SecurityContext.AllowPrivilegeEscalation != nil {
		t.Error("allowPrivilegeEscalation survived")
	}
	if c.SecurityContext != nil && c.SecurityContext.Capabilities != nil {
		t.Error("capabilities survived")
	}
	if len(c.Resources.Requests) != 0 {
		t.Errorf("requests = %v, want none: QoS returns to BestEffort", c.Resources.Requests)
	}
	if d.Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Errorf("filled = %q, want removed: nothing survived (BR-U05)", d.Annotations[v1alpha1.FilledAnnotation])
	}

	if row := undoRow(storedUndo(t, r, armed), "Deployment", "api"); row.Outcome != v1alpha1.OutcomeReverted {
		t.Errorf("outcome = %q, want Reverted", row.Outcome)
	}
}

// undoRow returns the plan row for one target, or the zero value.
func undoRow(u *v1alpha1.WorkloadHardeningUndo, kind, name string) v1alpha1.TargetStatus {
	for _, row := range u.Status.Plan {
		if row.Kind == kind && row.Name == name {
			return row
		}
	}
	return v1alpha1.TargetStatus{}
}

// AC-U06 and AC-U17: a partial undo rewrites the annotation to exactly the
// surviving records in the same request, and the target is still held.
func TestUndoPartialRewritesTheAnnotation(t *testing.T) {
	// A human raised the cpu request after the apply, so BR-U02 leaves it.
	edited := hardenedDeployment("tenant-a", "api", func(d *appsv1.Deployment) {
		d.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("500m")
	})

	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), edited)
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	armed := arm(t, r, u)
	if err := r.Reconcile(context.Background(), undoKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	d := storedTemplate(t, r.Kube, "tenant-a", "api")
	filled := d.Annotations[v1alpha1.FilledAnnotation]
	if filled != "spec.template.spec.containers[app].resources.requests.cpu=10m" {
		t.Errorf("filled =\n%q\nwant exactly the one surviving record", filled)
	}
	if got := d.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "500m" {
		t.Errorf("cpu = %s, want the human's 500m untouched", got.String())
	}
	if _, present := d.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]; present {
		t.Error("memory request survived; only cpu was edited")
	}
	// AC-U17: gated or edited, the target is still held.
	if d.Annotations[v1alpha1.SkipByAnnotation] != string(u.UID) {
		t.Error("a partially reverted target is not held")
	}
}

// AC-U09: preview issues every patch with DryRun, writes nothing, and
// publishes a hash per target. A target whose recorded field a human edits
// between preview and apply is Stale.
func TestUndoPreviewWritesNoDeletion(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), hardenedDeployment("tenant-a", "api"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}

	got := storedUndo(t, r, u)
	if got.Status.Phase != v1alpha1.PhasePreviewed {
		t.Fatalf("phase = %q (%s), want Previewed", got.Status.Phase, got.Status.Message)
	}
	row := undoRow(got, "Deployment", "api")
	if row.Hash == "" {
		t.Error("no change hash published; there is nothing to approve")
	}
	d := storedTemplate(t, r.Kube, "tenant-a", "api")
	if d.Annotations[v1alpha1.FilledAnnotation] == "" {
		t.Error("a preview removed the provenance annotation")
	}
	if d.Spec.Template.Spec.SecurityContext == nil {
		t.Error("a preview deleted a field")
	}

	// Now a human edits a recorded field. The deletion plan changes, so the
	// hash moves — which is exactly when the operator should look again.
	edited := storedTemplate(t, r.Kube, "tenant-a", "api")
	edited.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("500m")
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").Update(context.Background(), edited, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("editing the workload: %v", err)
	}

	armed := storedUndo(t, r, u).DeepCopy()
	armed.Spec.ApprovedPlan = []string{row.Hash}
	armed.Generation++
	r.Dyn = undoDynClient(t, armed)

	if err := r.Reconcile(context.Background(), undoKey(armed)); err == nil {
		// Stale is not retryable, so no error is correct here.
		_ = err
	}
	if got := undoRow(storedUndo(t, r, armed), "Deployment", "api"); got.Outcome != v1alpha1.OutcomeStale {
		t.Errorf("outcome = %q, want Stale: the approved change no longer describes this target", got.Outcome)
	}
}

// AC-U10: a workload deleted and recreated after hardening carries no
// annotation, so there is nothing to revert — correctly, because the new
// template was never patched. It is still selected, and still held.
func TestUndoRecreatedWorkloadHasNothingToRevert(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), deployment("tenant-a", "recreated"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := storedUndo(t, r, u)
	// FR-U06: an undo whose revert plan is empty is Applied, not Previewed.
	// There is nothing to approve and the request is complete.
	if got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase = %q (%s), want Applied: an empty revert plan is complete", got.Status.Phase, got.Status.Message)
	}
	if row := undoRow(got, "Deployment", "recreated"); row.Outcome != v1alpha1.OutcomeHeld {
		t.Errorf("outcome = %q, want Held", row.Outcome)
	}
}

// Error case: a malformed annotation is a finding naming the object, and
// other targets are unaffected.
func TestUndoMalformedAnnotationIsolatesOneTarget(t *testing.T) {
	broken := hardenedDeployment("tenant-a", "broken", func(d *appsv1.Deployment) {
		d.Annotations[v1alpha1.FilledAnnotation] = "this is not a path=value list\nnor is this"
	})

	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), broken, hardenedDeployment("tenant-a", "fine"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	armed := arm(t, r, u)
	if err := r.Reconcile(context.Background(), undoKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var reported bool
	for _, f := range storedUndo(t, r, armed).Status.Findings {
		if f.Name == "broken" && strings.Contains(f.Reason, "Unparseable") {
			reported = true
		}
	}
	if !reported {
		t.Error("the malformed annotation was not reported")
	}
	if d := storedTemplate(t, r.Kube, "tenant-a", "broken"); d.Annotations[v1alpha1.FilledAnnotation] == "" {
		t.Error("the malformed annotation was rewritten; it is never guessed at")
	}
	if d := storedTemplate(t, r.Kube, "tenant-a", "fine"); d.Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Error("the sound target was not reverted; one bad annotation must not stop the others")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./pkg/controller/ -run 'TestUndoReverts|TestUndoPartial|TestUndoPreview|TestUndoRecreated|TestUndoMalformed' -v`
Expected: FAIL.

- [ ] **Step 3: Write evaluate, execute and undoPhaseFor**

In `pkg/controller/undo.go`, follow `hardening.go`'s `evaluate` and `execute.go`'s `execute`/`preview`/`apply`, with these differences:

- The plan comes from `plan.Invert(target.Pod, target.Annotations[v1alpha1.FilledAnnotation], requests[namespace])`, which also returns the surviving annotation value.
- The patch body is `plan.Patch(p.Changes, annotations)` where `annotations` is `map[string]any{v1alpha1.FilledAnnotation: survivingOrNil(surviving)}`. Task 16 merges the hold into the same map.
- `survivingOrNil` returns `nil` for `""`, so the key is deleted rather than set to an empty string — an annotation present and empty asserts something different from an annotation absent.
- The outcome on success is `v1alpha1.OutcomeReverted`.
- `undoPhaseFor` is `phaseFor` with one change, commented at the branch:
  ```go
  	// An undo whose revert plan is empty is Applied, not Previewed: there is
  	// nothing to approve and the request is complete. It still holds its
  	// selected set, and deleting it still releases them — an object that
  	// reverted nothing is a pure bypass rule, which is a legitimate thing to
  	// create deliberately (FR-U06).
  	if !u.Armed() && revertable == 0 {
  		return v1alpha1.PhaseApplied, fmt.Sprintf("nothing to revert; %d workloads held", len(rows))
  	}
  ```
  where `revertable` counts rows carrying a non-empty `Hash`.

- [ ] **Step 4: Run the tests until green**

Run: `go test ./pkg/controller/ -run TestUndo -v`
Expected: every test PASS.

- [ ] **Step 5: Verify every write was dry-run first (NFR-U02)**

Add and run:

```go
// NFR-U02: no write before a dry-run of that same patch in the same pass. The
// fake records every action in order, so this is checkable rather than
// assumed.
func TestUndoDryRunsBeforeEveryWrite(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), hardenedDeployment("tenant-a", "api"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	armed := arm(t, r, u)
	if err := r.Reconcile(context.Background(), undoKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var lastDryRun []byte
	for _, p := range patchActions(t, r.Kube) {
		if len(p.PatchOptions.DryRun) > 0 {
			lastDryRun = p.GetPatch()
			continue
		}
		if string(p.GetPatch()) != string(lastDryRun) {
			t.Errorf("a write was issued that no dry-run preceded:\n write:   %s\n last dry-run: %s",
				p.GetPatch(), lastDryRun)
		}
		lastDryRun = nil
	}
}
```

Run: `go test ./pkg/controller/ -run TestUndoDryRuns -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/controller/undo.go pkg/controller/undo_test.go
git commit -m "feat(undo): preview and apply the revert

Reuses FR-03's machinery unmodified — the dry-run cache, the per-target
approval gate, Stale versus Unapproved, keep-what-succeeded — which is most of
why this feature is cheap. The plan comes from Invert instead of Build, and
the annotation it rewrites rides in the same request.

An empty revert plan is Applied, not Previewed: there is nothing to approve
and the request is complete. Such an object is a pure bypass rule, which is a
legitimate thing to create deliberately.

A missing annotation is written as null rather than as an empty string. An
annotation present and empty asserts something different from one that is
absent, and an operator reading the workload should see the difference.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 16: The hold, the re-assert and the release (BR-U09, BR-U10, FR-U06, AC-U13 to AC-U17, AC-U21, AC-U22)

**Files:**
- Modify: `pkg/controller/undo.go`
- Modify: `pkg/controller/undo_test.go`

**Interfaces:**
- Consumes: `holdFor`, `releaseFor`, `holder` (Task 13), `plan.MetadataPatch` (Task 10).
- Produces: nothing new. This task completes `UndoReconciler`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/controller/undo_test.go`:

```go
// AC-U14: the revert patch carries skip and skip-by in the same request, and a
// later WorkloadHardening reports that target excluded rather than planning
// it — with no change to 002.
func TestHoldExcludesTheTargetFromHardening(t *testing.T) {
	u := undo("tenant-a")
	ur := newUndoer(ns("tenant-a"), hardenedDeployment("tenant-a", "api"))
	ur.Dyn = undoDynClient(t, u)
	dryRunGuard(t, ur.Kube.(*fake.Clientset))

	if err := ur.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	armed := arm(t, ur, u)
	if err := ur.Reconcile(context.Background(), undoKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// One request, carrying all three: the deletions, the annotation rewrite
	// and the hold. A second patch would leave a window in which hardening
	// could refill what was just reverted.
	var writes int
	for _, p := range patchActions(t, ur.Kube) {
		if len(p.PatchOptions.DryRun) == 0 {
			writes++
		}
	}
	if writes != 1 {
		t.Errorf("%d writes, want 1: the revert, the annotation and the hold are one request (BR-U09)", writes)
	}

	d := storedTemplate(t, ur.Kube, "tenant-a", "api")
	if d.Annotations[v1alpha1.SkipAnnotation] != "true" || d.Annotations[v1alpha1.SkipByAnnotation] != string(u.UID) {
		t.Fatalf("annotations = %v, want skip and skip-by", d.Annotations)
	}

	// Now hardening, on the same workload, through 002's own untouched code.
	w := hardening("tenant-a")
	hr := newHardener(ns("tenant-a"), d)
	hr.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, hr.Kube.(*fake.Clientset))
	if err := hr.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("hardening: %v", err)
	}

	got := storedHardening(t, hr, w)
	if len(got.Status.Plan) != 0 {
		t.Errorf("hardening planned %d targets, want none: the workload is held", len(got.Status.Plan))
	}
	var excluded bool
	for _, f := range got.Status.Findings {
		if f.Name == "api" && strings.Contains(f.Reason, v1alpha1.SkipAnnotation) {
			excluded = true
		}
	}
	if !excluded {
		t.Errorf("hardening did not report api as excluded:\n%+v", got.Status.Findings)
	}
}

// AC-U21: a selected workload with no filled record is held by a
// metadata-only patch that starts no rollout, and an unarmed undo holds its
// whole selected set while reverting nothing.
func TestUnarmedUndoHoldsWithoutReverting(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"),
		hardenedDeployment("tenant-a", "hardened"),
		deployment("tenant-a", "never-hardened"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	for _, name := range []string{"hardened", "never-hardened"} {
		d := storedTemplate(t, r.Kube, "tenant-a", name)
		if d.Annotations[v1alpha1.SkipAnnotation] != "true" {
			t.Errorf("%s: not held; an unarmed undo holds its whole selected set (BR-U09)", name)
		}
		if d.Annotations[v1alpha1.SkipByAnnotation] != string(u.UID) {
			t.Errorf("%s: skip-by = %q, want this object's UID", name, d.Annotations[v1alpha1.SkipByAnnotation])
		}
	}

	held := storedTemplate(t, r.Kube, "tenant-a", "hardened")
	if held.Spec.Template.Spec.Containers[0].SecurityContext.Capabilities == nil {
		t.Error("an unarmed undo deleted a field; a preview reverts nothing")
	}
	if held.Annotations[v1alpha1.FilledAnnotation] == "" {
		t.Error("an unarmed undo removed the provenance annotation")
	}

	// The hold patch must not reach spec, or it changes the pod-template hash
	// and restarts every pod for two annotations — which is the whole reason a
	// hold is not gated by approvedPlan the way a revert is.
	for _, p := range patchActions(t, r.Kube) {
		if strings.Contains(string(p.GetPatch()), `"spec"`) {
			t.Errorf("a hold patch reached spec:\n%s", p.GetPatch())
		}
	}
}

// AC-U22: a skip stripped by hand is re-asserted on the next resync, and an
// object whose holds are all intact issues no writes.
func TestHoldIsReassertedAndOtherwiseSilent(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if d := storedTemplate(t, r.Kube, "tenant-a", "api"); d.Annotations[v1alpha1.SkipAnnotation] != "true" {
		t.Fatal("the first pass did not hold the workload")
	}

	// A resync with everything intact. The object is Applied, and holdFor
	// returns nil for a target it already holds, so nothing is written.
	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), undoKey(storedUndo(t, r, u))); err != nil {
		t.Fatalf("intact resync: %v", err)
	}
	for _, p := range patchActions(t, r.Kube) {
		if len(p.PatchOptions.DryRun) == 0 {
			t.Errorf("an intact object wrote on resync:\n%s", p.GetPatch())
		}
	}

	// Someone strips the skip by hand. Nothing else would notice: an Applied
	// object is otherwise inert, and the next hardening would pick the
	// workload up.
	stripped := storedTemplate(t, r.Kube, "tenant-a", "api")
	delete(stripped.Annotations, v1alpha1.SkipAnnotation)
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").Update(context.Background(), stripped, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("stripping the annotation: %v", err)
	}

	if err := r.Reconcile(context.Background(), undoKey(storedUndo(t, r, u))); err != nil {
		t.Fatalf("repair resync: %v", err)
	}
	if d := storedTemplate(t, r.Kube, "tenant-a", "api"); d.Annotations[v1alpha1.SkipAnnotation] != "true" {
		t.Error("the hold was not re-asserted; it would silently lapse and the next hardening would refill")
	}
}

// AC-U15 and AC-U20: deleting the undo removes both annotations and the
// finalizer; a skip with no skip-by is a human's and survives the whole
// lifecycle, marked by nobody and removed by nobody.
func TestDeleteReleasesOnlyWhatItWrote(t *testing.T) {
	humans := hardenedDeployment("tenant-a", "human-exempt", func(d *appsv1.Deployment) {
		d.Annotations[v1alpha1.SkipAnnotation] = "true" // no skip-by: a human set it
	})
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), hardenedDeployment("tenant-a", "api"), humans)
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// AC-U20: the human's workload is reverted but never marked.
	if d := storedTemplate(t, r.Kube, "tenant-a", "human-exempt"); d.Annotations[v1alpha1.SkipByAnnotation] != "" {
		t.Errorf("skip-by = %q on a human's exemption; not ours to mark", d.Annotations[v1alpha1.SkipByAnnotation])
	}

	// Delete the undo.
	deleting := storedUndo(t, r, u).DeepCopy()
	now := metav1.NewTime(time.Unix(1700000000, 0))
	deleting.DeletionTimestamp = &now
	r.Dyn = undoDynClient(t, deleting)

	if err := r.Reconcile(context.Background(), undoKey(deleting)); err != nil {
		t.Fatalf("delete pass: %v", err)
	}

	if d := storedTemplate(t, r.Kube, "tenant-a", "api"); d.Annotations[v1alpha1.SkipAnnotation] != "" ||
		d.Annotations[v1alpha1.SkipByAnnotation] != "" {
		t.Errorf("annotations = %v, want both released", d.Annotations)
	}
	if d := storedTemplate(t, r.Kube, "tenant-a", "human-exempt"); d.Annotations[v1alpha1.SkipAnnotation] != "true" {
		t.Error("a human's hand-set exemption was removed; it was never ours")
	}
	if got := storedUndo(t, r, deleting); len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want cleared so the delete completes", got.Finalizers)
	}
}

// Error case: a target deleted while an undo holding it still exists. Nothing
// to release, and the finalizer must not block the delete.
func TestDeleteWithAMissingTargetStillClears(t *testing.T) {
	u := undo("tenant-a")
	r := newUndoer(ns("tenant-a"), hardenedDeployment("tenant-a", "api"))
	r.Dyn = undoDynClient(t, u)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// The workload goes away while the undo still holds it.
	if err := r.Kube.AppsV1().Deployments("tenant-a").
		Delete(context.Background(), "api", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting the target: %v", err)
	}

	deleting := storedUndo(t, r, u).DeepCopy()
	now := metav1.NewTime(time.Unix(1700000000, 0))
	deleting.DeletionTimestamp = &now
	r.Dyn = undoDynClient(t, deleting)

	if err := r.Reconcile(context.Background(), undoKey(deleting)); err != nil {
		t.Fatalf("delete pass: %v; a missing target is nothing to release, not an error", err)
	}
	if got := storedUndo(t, r, deleting); len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want cleared: a delete must not hang on a workload that is gone", got.Finalizers)
	}
}

// AC-U16: a hardening and an undo both naming a namespace converge, and
// neither churns its hashes across resyncs.
func TestHardeningAndUndoConverge(t *testing.T) {
	u := undo("tenant-a")
	ur := newUndoer(ns("tenant-a"), hardenedDeployment("tenant-a", "api"))
	ur.Dyn = undoDynClient(t, u)
	dryRunGuard(t, ur.Kube.(*fake.Clientset))

	if err := ur.Reconcile(context.Background(), undoKey(u)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	armed := arm(t, ur, u)
	if err := ur.Reconcile(context.Background(), undoKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The reverted workload, now held, through three hardening passes and
	// three undo passes. Nothing may change after the first.
	reverted := storedTemplate(t, ur.Kube, "tenant-a", "api")
	w := hardening("tenant-a")
	hr := newHardener(ns("tenant-a"), reverted)
	hr.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, hr.Kube.(*fake.Clientset))

	var hashes []string
	for pass := 0; pass < 3; pass++ {
		if err := hr.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("hardening pass %d: %v", pass, err)
		}
		if err := ur.Reconcile(context.Background(), undoKey(storedUndo(t, ur, armed))); err != nil {
			t.Fatalf("undo pass %d: %v", pass, err)
		}
		var h string
		for _, row := range storedUndo(t, ur, armed).Status.Plan {
			h += row.Hash + ","
		}
		hashes = append(hashes, h)
	}
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Errorf("the undo churned its hashes: pass 0 %q, pass %d %q", hashes[0], i, hashes[i])
		}
	}
	if len(storedHardening(t, hr, w).Status.Plan) != 0 {
		t.Error("hardening re-planned a held workload; the loop is not closed")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./pkg/controller/ -run 'TestHold|TestUnarmed|TestDelete|TestHardeningAndUndo' -v`
Expected: FAIL.

- [ ] **Step 3: Merge the hold into the patch**

In `pkg/controller/undo.go`:

1. In the per-target path, build the annotation map once and use it for both patch shapes:
   ```go
   	hold := holdFor(target.Annotations, string(u.UID))
   	switch {
   	case len(p.Changes) > 0 && u.Approved(row.Hash):
   		// One request: the deletions, the rewritten provenance and the hold.
   		// A second patch would leave a window in which a hardening pass could
   		// refill what was just reverted (BR-U05, BR-U09).
   		annotations := map[string]any{v1alpha1.FilledAnnotation: survivingOrNil(surviving)}
   		for k, v := range hold {
   			annotations[k] = v
   		}
   		body, err := plan.Patch(p.Changes, annotations)
   		// ... dry-run, then write, outcome Reverted
   	case len(hold) > 0:
   		// Metadata only. Touches nothing under spec.template, so it changes no
   		// pod-template hash and starts no rollout — which is why a hold is not
   		// gated by approvedPlan the way a revert is.
   		body, err := plan.MetadataPatch(hold)
   		// ... dry-run, then write, outcome Held
   	default:
   		// Already held, nothing approved. No API call at all.
   	}
   ```
2. Set `row.Holder` and `row.HeldByMe` from `holder(target.Annotations)` on every row, so an operator can see which object a `kubectl delete` would release.
3. Replace Task 14's deletion-branch TODO:
   ```go
   	if u.DeletionTimestamp != nil {
   		return r.release(ctx, logger, u)
   	}
   ```
   `release` discovers the selected set, issues a `plan.MetadataPatch(releaseFor(...))` for every target whose marker is this object's, and then clears the finalizer with a merge patch scoped to `metadata` — read `isolation.go`'s `patchFinalizers` and reuse its shape. A target that no longer exists is not an error: there is nothing to release and the finalizer clears.
4. Persist `UndoFinalizer` before the first hold is written, for the reason 001 persists its own before the first policy write.

- [ ] **Step 4: Run the tests until green**

Run: `go test ./pkg/controller/ -run 'TestHold|TestUnarmed|TestDelete|TestHardeningAndUndo|TestUndo' -v`
Expected: every test PASS.

- [ ] **Step 5: Run the whole suite and the coverage floor**

Run: `go test ./... -race && make cover`
Expected: every package `ok`, coverage at or above 90%.

- [ ] **Step 6: Verify 002 was not edited**

```bash
git diff --stat HEAD~3 -- pkg/controller/hardening.go pkg/controller/execute.go pkg/controller/hardening_test.go
```

Expected: only the `execute.go` call-site change from Task 10, the `discoverWorkloads` call from Task 12, and the test-only `patchActions`/`storedTemplate` signature change. Any changed assertion in `hardening_test.go` means the hold moved 002's behaviour, which BR-U10 forbids.

- [ ] **Step 7: Commit**

```bash
git add pkg/controller/undo.go pkg/controller/undo_test.go
git commit -m "feat(undo): the hold, its re-assert, and the release on delete

The hold rides in the same request as the revert — BR-U05 is already rewriting
metadata.annotations there, so it costs no extra API call and leaves no window
in which a hardening pass could refill what was just reverted. A selected
workload with nothing to revert gets a metadata-only patch instead, which
touches no spec and so starts no rollout: that is why a hold is not gated by
approvedPlan the way a revert is.

Re-asserted on every pass, because nothing else would keep it: an Applied
object is otherwise inert, so a skip someone strips by hand would silently
lapse and the next hardening would pick the workload up. holdFor returns nil
for a target this object already holds, so an intact object still issues no
writes.

The release takes back only what this object wrote. A skip with no skip-by is
a human's exemption and survives the undo's whole lifecycle — marked by
nobody, removed by nobody. The finalizer clears even where the workload is
gone: there is nothing to release and a delete must not hang on it.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

### Task 17: Wiring, RBAC and manifests (FR-U05, NFR-U01, NFR-U03)

**Files:**
- Modify: `pkg/controller/controller.go`
- Modify: `pkg/controller/controller_test.go`
- Modify: `cmd/main/main.go`
- Modify: `deploy/rbac.yaml`
- Create: `deploy/samples/undo.yaml`

**Interfaces:**
- Consumes: `UndoReconciler` from Tasks 7–9, `v1alpha1.UndoResource` from Task 11.
- Produces: `controller.New(kube, dyn, iso, hardening, undo, resync)` — one more parameter.

- [ ] **Step 1: Write the failing test**

Add to `pkg/controller/controller_test.go`:

```go
// The third resource is watched, and its keys reach its own reconciler. One
// queue, one worker, three resources — so a hardening reconcile and an undo
// reconcile never interleave, which is what puts the hold in place before any
// pass could act on the reopened gaps.
func TestQueueRoutesAllThreeResources(t *testing.T) {
	for _, resource := range []string{
		v1alpha1.Resource.Resource,
		v1alpha1.HardeningResource.Resource,
		v1alpha1.UndoResource.Resource,
	} {
		if _, ok := c.reconcile[resource]; !ok {
			t.Errorf("no reconciler registered for %q", resource)
		}
	}
}
```

Follow the existing test's construction of `c` — read `controller_test.go` first.

- [ ] **Step 2: Add the third informer**

In `pkg/controller/controller.go`:

```go
func New(
	kube kubernetes.Interface,
	dyn dynamic.Interface,
	iso *IsolationReconciler,
	hardening *HardeningReconciler,
	undo *UndoReconciler,
	resync time.Duration,
) (*Controller, error) {
```

Add `v1alpha1.UndoResource.Resource: undo.Reconcile` to the `reconcile` map, and `v1alpha1.UndoResource` to the GVR slice the dynamic factory loops over. Nothing else changes: a third resource costs a third informer, not a second process, queue or worker.

- [ ] **Step 3: Wire main**

In `cmd/main/main.go`, after the hardening reconciler:

```go
	// The same clients, the same protected set and the same timeouts.
	undo := &controller.UndoReconciler{
		Kube:      kube,
		Dyn:       dyn,
		Protected: protected,
		Timeout:   *timeout,
		Now:       time.Now,
	}
```

and pass it to `controller.New`.

- [ ] **Step 4: Extend RBAC**

In `deploy/rbac.yaml`, append to the ClusterRole:

```yaml
  # Workload hardening undo (003). No new verbs on any core resource: the hold
  # and the release are annotation patches on objects the rules above already
  # permit patching, and BR-U04's enforce label is on a namespace already read.
  - apiGroups: ["hardening.acme.corp"]
    resources: ["workloadhardeningundos"]
    # update, unlike workloadhardenings: this kind carries a finalizer, and the
    # release is what makes a workload eligible for hardening again.
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["hardening.acme.corp"]
    resources: ["workloadhardeningundos/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: ["hardening.acme.corp"]
    resources: ["workloadhardeningundos/finalizers"]
    verbs: ["update"]
```

- [ ] **Step 5: Add the sample**

Create `deploy/samples/undo.yaml`:

```yaml
# Reverts the hardening applied by deploy/samples/hardening.yaml, and holds
# those workloads out of future hardening until this object is deleted.
#
# Unarmed: creating it holds the selected set and previews the revert. Copy the
# hashes out of status.plan into spec.approvedPlan to perform it.
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardeningUndo
metadata:
  name: tenant-rollback
  namespace: isolation-system
spec:
  namespaces: [harden-a, harden-b]
  approvedPlan: []
```

- [ ] **Step 6: Run the suite and verify the manifests**

```bash
go test ./... -race && make cover
kubectl apply --dry-run=client -f deploy/rbac.yaml -f deploy/samples/undo.yaml
```

Expected: every package `ok`, coverage at or above 90%, both manifests validate.

- [ ] **Step 7: Verify NFR-U03's claim literally**

```bash
git diff deploy/rbac.yaml | grep '^+' | grep -v 'hardening.acme.corp\|workloadhardeningundos\|verbs\|apiGroups\|resources\|^+++\|^+ *#'
```

Expected: **no output**. Any added rule naming a core or `apps` resource breaks "no new verbs on any core resource".

- [ ] **Step 8: Commit**

```bash
git add pkg/controller/controller.go pkg/controller/controller_test.go cmd/main/main.go deploy/rbac.yaml deploy/samples/undo.yaml
git commit -m "feat(undo): a third watched resource on the same queue and worker

One more informer, one more entry in the resource-to-reconciler map, and the
same single worker — so a hardening reconcile and an undo reconcile never
interleave, which is what puts the hold in place before any pass could act on
the reopened gaps.

RBAC gains the new kind, its status and its finalizers subresource, and update
on the resource itself because this kind carries a finalizer. No new verbs on
any core resource: the hold and the release are annotation patches on objects
the existing rules already permit patching, and the enforce label is on a
namespace already read (NFR-U03).

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 18: Live verification on kind (NFR-U04, AC-U03, AC-U04, AC-U05, AC-U08, AC-U11)

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

In `.github/workflows/ci.yml`, the `kind` job already runs `make verify-crd` and `make verify`, and Task 11 and Step 3 above added the undo to both aggregates — so nothing new is needed. Confirm:

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

---

## Part C — Metrics endpoint and Grafana (Tasks 19–23)

Independent of Parts A and B except for two touch points: Task 22 adds a step to the workflow Task 5 creates, and `hardening_targets_reverted_total` stays at zero until Part B exists. It can be built before, after or alongside them.

---

### Task 19: The metrics package (Metrics endpoint — 001 G-07, 002 G-07)

**Files:**
- Create: `pkg/metrics/metrics.go`
- Create: `pkg/metrics/metrics_test.go`
- Modify: `go.mod`, `go.sum`, `vendor/` (regenerated)

**Interfaces:**
- Consumes: nothing.
- Produces, all used by Tasks 2 and 3:
  - `metrics.Handler() http.Handler`
  - `metrics.Reconcile(resource string, phase v1alpha1.Phase)`
  - `metrics.TargetPatched()`
  - `metrics.TargetReverted()`
  - `metrics.DryRunRefused()`
  - `metrics.ApplyFailed()`
  - `metrics.QueueDepth(n int)`

- [ ] **Step 1: Add the dependency and vendor it**

```bash
go get github.com/prometheus/client_golang@latest
go mod vendor
git status --short vendor/ | head -5
```

Expected: `vendor/github.com/prometheus/...` appears. Record the count for the commit message:

```bash
grep -c '^# ' vendor/modules.txt   # was 48
```

- [ ] **Step 2: Write the failing test**

Create `pkg/metrics/metrics_test.go`:

```go
package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// body scrapes the handler and returns the exposition text.
func body(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// Every collector is reachable through the handler, under the name a dashboard
// will hardcode. A renamed metric is a silently empty panel, so the names are
// asserted literally rather than derived.
func TestHandlerExposesEveryCollector(t *testing.T) {
	Reconcile("workloadhardenings", v1alpha1.PhaseApplied)
	TargetPatched()
	TargetReverted()
	DryRunRefused()
	ApplyFailed()
	QueueDepth(7)

	got := body(t)
	for _, name := range []string{
		`hardening_reconcile_total{phase="Applied",resource="workloadhardenings"} 1`,
		"hardening_targets_patched_total 1",
		"hardening_targets_reverted_total 1",
		"hardening_dryrun_refusals_total 1",
		"hardening_apply_failures_total 1",
		"hardening_queue_depth 7",
	} {
		if !strings.Contains(got, name) {
			t.Errorf("missing from /metrics:\n  %s", name)
		}
	}
}

// Review Focus 2: phase is a closed set and resource is one of three. Neither
// a namespace nor an object name may ever reach a label, because an unbounded
// label value makes a counter grow without limit for the process's lifetime.
// This pins the signature: Reconcile takes a Phase, not a string.
func TestReconcileLabelsAreBounded(t *testing.T) {
	for _, phase := range []v1alpha1.Phase{
		v1alpha1.PhasePending, v1alpha1.PhaseRejected, v1alpha1.PhasePreviewed,
		v1alpha1.PhaseApplied, v1alpha1.PhasePartiallyApplied,
	} {
		Reconcile("workloadhardenings", phase)
	}

	got := body(t)
	series := strings.Count(got, "hardening_reconcile_total{")
	if series > 8 {
		t.Errorf("hardening_reconcile_total has %d series; phase x resource is bounded well below that", series)
	}
}

// Review Focus 1: the collectors live in package-level vars, so a duplicate
// registration panics at init and the controller dies before it serves. Two
// Handler() calls must be safe, and so must a second scrape.
func TestHandlerIsReusable(t *testing.T) {
	_ = body(t)
	_ = body(t)
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./pkg/metrics/ -v`
Expected: FAIL — `no required module provides package .../pkg/metrics` or `undefined: Handler`.

- [ ] **Step 4: Write the implementation**

Create `pkg/metrics/metrics.go`:

```go
// Package metrics publishes what this controller does, in the Prometheus text
// exposition format, from a registry of its own.
//
// A registry of its own rather than the default one: prometheus.DefaultRegisterer
// carries Go runtime and process collectors that a vendored library may also
// register, and a duplicate registration panics. Six series and the two
// standard collectors, named explicitly, is the whole surface.
//
// NFR-01 forbids new dependencies and is waived here. Six counters in the text
// format really are forty lines of net/http and sync/atomic — and a hand-rolled
// exposition is forty lines every reviewer has to check for escaping and
// "# TYPE" ordering before trusting, against a library every scraper already
// assumes. See the Metrics endpoint section of specs/003-bonus/spec.md.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

var registry = prometheus.NewRegistry()

var factory = promauto.With(registry)

var (
	// reconciles is labelled by resource and phase, and by nothing else.
	// A namespace or an object name here would be an unbounded label value:
	// the series count would grow with the cluster and never shrink, which is
	// the standard way a metrics endpoint becomes the leak it was added to
	// detect.
	reconciles = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "hardening_reconcile_total",
		Help: "Reconcile passes that reached a terminal decision, by resource and resulting phase.",
	}, []string{"resource", "phase"})

	targetsPatched = factory.NewCounter(prometheus.CounterOpts{
		Name: "hardening_targets_patched_total",
		Help: "Workload templates patched by WorkloadHardening.",
	})

	targetsReverted = factory.NewCounter(prometheus.CounterOpts{
		Name: "hardening_targets_reverted_total",
		Help: "Workload templates reverted by WorkloadHardeningUndo.",
	})

	dryRunRefusals = factory.NewCounter(prometheus.CounterOpts{
		Name: "hardening_dryrun_refusals_total",
		Help: "Dry-run patches the API server refused.",
	})

	applyFailures = factory.NewCounter(prometheus.CounterOpts{
		Name: "hardening_apply_failures_total",
		Help: "Patches that failed after their dry-run was accepted.",
	})

	// A gauge, not a counter: depth goes down as well as up. The spec calls
	// all six counters; five of them are.
	queueDepth = factory.NewGauge(prometheus.GaugeOpts{
		Name: "hardening_queue_depth",
		Help: "Items currently in the work queue.",
	})
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Handler serves the registry. Safe to call more than once.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// Reconcile records one pass that reached a phase. The phase type rather than
// a string, so a caller cannot widen the label set by passing a message.
func Reconcile(resource string, phase v1alpha1.Phase) {
	reconciles.WithLabelValues(resource, string(phase)).Inc()
}

// TargetPatched records one workload template patched.
func TargetPatched() { targetsPatched.Inc() }

// TargetReverted records one workload template reverted.
func TargetReverted() { targetsReverted.Inc() }

// DryRunRefused records one dry-run the API server refused.
func DryRunRefused() { dryRunRefusals.Inc() }

// ApplyFailed records one patch that failed after a clean dry-run.
func ApplyFailed() { applyFailures.Inc() }

// QueueDepth publishes the current queue length.
func QueueDepth(n int) { queueDepth.Set(float64(n)) }
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/metrics/ -v`
Expected: all three tests PASS.

- [ ] **Step 6: Verify nothing else moved**

Run: `go build ./... && go test ./... -race && make cover`
Expected: five packages `ok`, coverage at or above 90%.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum vendor pkg/metrics
git commit -m "feat(metrics): six series on a private registry

NFR-01 waived for prometheus/client_golang, and for nothing else. The
hand-rolled version really is forty lines of net/http and sync/atomic with no
go.mod change — and forty lines every reviewer has to check for escaping and
'# TYPE' ordering before trusting, against a library every scraper assumes.
The cost is a visibly larger vendor/, which is the trade being accepted.

A private registry rather than the default one: a vendored library that also
registers the Go collector would panic the process at init.

Labels are resource and phase, both closed sets. Reconcile takes a Phase
rather than a string so a caller cannot put a namespace in a label and grow
the series count with the cluster.

Closes 001's G-07 and 002's G-07 at the endpoint; the wiring is next.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 20: Instrument the reconcilers (Metrics endpoint)

**Files:**
- Modify: `pkg/controller/hardening.go` (the `setStatus` call sites and `evaluate`'s tail)
- Modify: `pkg/controller/execute.go` (`preview`, `apply`)
- Modify: `pkg/controller/isolation.go` (isolation's phase transitions)
- Modify: `pkg/controller/controller.go` (`processNext`)
- Modify: `pkg/controller/hardening_test.go`

**Interfaces:**
- Consumes: every function from Task 19.
- Produces: nothing new. `hardening_targets_reverted_total` stays at zero until Part B's apply path calls `metrics.TargetReverted()`; it is registered here so a dashboard built now does not have to change then.

If Part A has not run, `pkg/controller/isolation.go` is still `pkg/controller/reconcile.go` and `setStatus` on the hardening reconciler is still `setHardeningStatus`. Use whichever names are in the tree.

- [ ] **Step 1: Write the failing test**

Add to `pkg/controller/hardening_test.go`:

```go
// One armed pass over one target increments the patch counter once and the
// reconcile counter once, under the phase the object reached. Counting patches
// anywhere but the one place that patches would double-count a retry.
func TestMetricsCountPatchesAndPhases(t *testing.T) {
	before := counterValue(t, "hardening_targets_patched_total")

	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := counterValue(t, "hardening_targets_patched_total"); got != before {
		t.Errorf("a preview incremented the patch counter: %v -> %v", before, got)
	}

	previewed := storedHardening(t, r, w)
	armed := previewed.DeepCopy()
	armed.Spec.ApprovedPlan = []string{previewed.Status.Plan[0].Hash}
	r.Dyn = hardeningDynClient(t, armed)

	if err := r.Reconcile(context.Background(), hardeningKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := counterValue(t, "hardening_targets_patched_total"); got != before+1 {
		t.Errorf("hardening_targets_patched_total = %v, want %v after one patch", got, before+1)
	}
	if counterValue(t, `hardening_reconcile_total{phase="Applied",resource="workloadhardenings"}`) == 0 {
		t.Error("no reconcile counted under phase Applied")
	}
}
```

And the helper it needs, in the same file:

```go
// counterValue scrapes the metrics handler and reads one series by its exact
// exposition prefix. Scraping rather than reaching into the collector, so the
// test fails the same way a dashboard would.
func counterValue(t *testing.T, series string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, series+" ") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimPrefix(line, series+" "), 64)
		if err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		return v
	}
	return 0
}
```

Add `net/http`, `net/http/httptest`, `strconv` and the `pkg/metrics` import.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/controller/ -run TestMetricsCountPatchesAndPhases -v`
Expected: FAIL — `hardening_targets_patched_total = 0, want 1`.

- [ ] **Step 3: Instrument the four call sites**

In `pkg/controller/execute.go`, in `apply`, immediately after the real patch succeeds and before `row.Outcome = v1alpha1.OutcomePatched`:

```go
	metrics.TargetPatched()
```

In the same file, in both `preview` and `apply`, inside the `if err := t.patch(ctx, body, dryRun()); err != nil {` block, as the first statement:

```go
		metrics.DryRunRefused()
```

And in `apply`, inside the real patch's error block:

```go
		metrics.ApplyFailed()
```

In `pkg/controller/hardening.go`, in `evaluate`, immediately after `phase, message := phaseFor(w, rows)`:

```go
	metrics.Reconcile(v1alpha1.HardeningResource.Resource, phase)
```

And in the two early-return branches that set a phase without reaching that line — the `errors.As(err, &rej)` branch and the conversion-failure branch in `Reconcile` — add the same call with `v1alpha1.PhaseRejected`.

In `pkg/controller/isolation.go`, add one `metrics.Reconcile(v1alpha1.Resource.Resource, phase)` beside each `setStatus` call that names a phase.

Add the `pkg/metrics` import to each file.

- [ ] **Step 4: Publish queue depth**

In `pkg/controller/controller.go`, in `processNext`, immediately after `defer c.queue.Done(key)`:

```go
	// Published here rather than on Add: the queue de-duplicates, so its
	// length only means anything once, from the consumer's side.
	metrics.QueueDepth(c.queue.Len())
```

- [ ] **Step 5: Run the tests**

Run: `go test ./pkg/controller/ -run TestMetricsCountPatchesAndPhases -v`
Expected: PASS.

Run: `go test ./... -race && make cover`
Expected: five packages `ok`, coverage at or above 90%.

- [ ] **Step 6: Verify nothing counts a patch twice**

```bash
grep -rn 'metrics.TargetPatched()' pkg/
```

Expected: exactly one hit, in `execute.go`'s `apply`. A second call site anywhere — a retry path, a status writer — would double-count.

- [ ] **Step 7: Commit**

```bash
git add pkg/controller
git commit -m "feat(metrics): count patches, refusals, failures, phases and queue depth

One call site each. TargetPatched lives in the single function that patches,
so a retry cannot double-count it, and queue depth is published from the
consumer because the queue de-duplicates and its length only means something
once.

hardening_targets_reverted_total stays at zero until the undo wires it. It is
registered now so a dashboard built today does not change then.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 21: Serve it from the binary (Metrics endpoint)

**Files:**
- Modify: `cmd/main/main.go`

**Interfaces:**
- Consumes: `metrics.Handler()` from Task 19.
- Produces: a `-metrics-addr` flag, default `:8080`. Task 22's manifest and Task 23's scrape config both name that port.

- [ ] **Step 1: Add the flag**

In the `flag` block in `main`:

```go
		metricsAddr = flag.String("metrics-addr", ":8080", "address for the Prometheus metrics endpoint; empty disables it")
```

- [ ] **Step 2: Serve it, and shut it down with the controller (Review Focus 3)**

After the reconcilers are built and before `c.Run(ctx)`:

```go
	// Served on its own listener, shut down with the controller. A bare
	// `go http.ListenAndServe` would ignore ctx, so every rollout would wait
	// out the kubelet's grace period instead of exiting on SIGTERM.
	if *metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		server := &http.Server{Addr: *metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			logger.Info("Serving metrics", "addr", *metricsAddr)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error(err, "Metrics endpoint stopped")
			}
		}()
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		}()
	}
```

Add `errors` and `net/http` to the imports, plus `pkg/metrics`.

`klog.FlushAndExit` in the error paths below calls `os.Exit`, which skips deferred functions — that is fine and deliberate: those paths are already failing to start.

- [ ] **Step 3: Verify it builds and serves**

```bash
go build -o /tmp/controller ./cmd/main
/tmp/controller -kubeconfig=/dev/null -metrics-addr=:18080 &
pid=$!
for i in $(seq 1 20); do curl -sf localhost:18080/metrics >/dev/null 2>&1 && break; sleep 0.2; done
curl -s localhost:18080/metrics | grep -c '^hardening_'
kill $pid
```

Expected: a non-zero count. The controller will fail to reach a cluster with that kubeconfig and log about it; the metrics listener comes up regardless, which is the point.

- [ ] **Step 4: Verify it exits on SIGTERM (Review Focus 3)**

```bash
/tmp/controller -kubeconfig=/dev/null -metrics-addr=:18081 &
pid=$!
sleep 1
kill -TERM $pid
for i in $(seq 1 30); do kill -0 $pid 2>/dev/null || break; sleep 0.2; done
if kill -0 $pid 2>/dev/null; then echo "FAIL: still running 6s after SIGTERM"; kill -9 $pid; else echo "ok: exited on SIGTERM"; fi
```

Expected: `ok: exited on SIGTERM`.

- [ ] **Step 5: Commit**

```bash
git add cmd/main/main.go
git commit -m "feat(metrics): serve /metrics from the controller binary

-metrics-addr, default :8080, empty to disable. Its own listener and its own
Shutdown on the way out: a bare go ListenAndServe ignores the signal context,
so every rollout would wait out the kubelet's grace period instead of exiting
on SIGTERM.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 22: Manifest, Service and a CI smoke test (Metrics endpoint)

**Files:**
- Modify: `deploy/controller.yaml`
- Create: `deploy/metrics-service.yaml`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the `:8080` default from Task 21.
- Produces: a `Service/network-isolation-metrics` in `isolation-system` on port 8080, and pod annotations Task 23's Prometheus scrapes.

- [ ] **Step 1: Add the port and the scrape annotations**

In `deploy/controller.yaml`, add to the pod template's `metadata`:

```yaml
    metadata:
      labels: {app: network-isolation}
      annotations:
        # Scraped by annotation rather than by ServiceMonitor: the raw
        # prometheus chart ships no operator and no CRDs, and its default
        # kubernetes_sd config honours these three (see hack/grafana.sh).
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: /metrics
```

And to the container:

```yaml
          ports:
            - name: metrics
              containerPort: 8080
              protocol: TCP
```

Leave `args` as they are — `:8080` is the flag's default.

- [ ] **Step 2: Add the Service**

Create `deploy/metrics-service.yaml`:

```yaml
# A Service for the metrics port only. The controller serves no other traffic
# and takes no requests: nothing reaches it except a scraper.
apiVersion: v1
kind: Service
metadata:
  name: network-isolation-metrics
  namespace: isolation-system
  labels: {app: network-isolation}
spec:
  selector: {app: network-isolation}
  ports:
    - name: metrics
      port: 8080
      targetPort: metrics
      protocol: TCP
```

- [ ] **Step 3: Apply it from `deploy`**

In the `Makefile`'s `deploy` recipe, after the `kubectl apply -f deploy/controller.yaml` line:

```make
	kubectl apply -f deploy/metrics-service.yaml
```

- [ ] **Step 4: Add the CI smoke test (Review Focus 4)**

In `.github/workflows/ci.yml`, in the `kind` job, after the step that runs `make verify`:

```yaml
      - name: Metrics endpoint
        run: |
          set -euo pipefail
          kubectl -n isolation-system rollout status deployment/network-isolation --timeout=120s
          # Assert a known series, not a 200. A port that is bound but serving
          # an empty body looks identical to a healthy one from the manifest.
          body=$(kubectl -n isolation-system run metrics-probe \
            --rm -i --restart=Never --image=curlimages/curl:8.11.1 --quiet -- \
            -sS --max-time 10 http://network-isolation-metrics:8080/metrics)
          for want in hardening_reconcile_total hardening_targets_patched_total \
                      hardening_dryrun_refusals_total hardening_queue_depth; do
            printf '%s' "$body" | grep -q "^# TYPE $want" || { echo "missing: $want"; exit 1; }
          done
          echo "ok: /metrics serves every declared series"
```

This runs after `make verify`, so the counters have been exercised by a real reconcile rather than read at zero.

- [ ] **Step 5: Verify the manifests parse and the selector matches**

```bash
kubectl apply --dry-run=client -f deploy/controller.yaml -f deploy/metrics-service.yaml
svc=$(awk '/^  selector:/{print $2 $3}' deploy/metrics-service.yaml)
pod=$(awk '/^      labels:/{print $2 $3; exit}' deploy/controller.yaml)
echo "service selector=$svc  pod labels=$pod"
```

Expected: both manifests validate, and the two label expressions agree on `app:network-isolation`. A Service whose selector matches nothing returns a connection refused that reads exactly like a dead controller.

- [ ] **Step 6: Commit**

```bash
git add deploy/controller.yaml deploy/metrics-service.yaml Makefile .github/workflows/ci.yml
git commit -m "feat(metrics): expose the port, add a Service, and smoke it in CI

Scrape annotations rather than a ServiceMonitor: the raw prometheus chart
ships no operator and no CRDs, so the annotations its default kubernetes_sd
config already honours are the smaller of the two options.

CI asserts four series by name after make verify has exercised them, not a
200 — a port that is bound and serving an empty body looks identical to a
healthy one from the manifest.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 23: Prometheus and Grafana from one script (Grafana)

**Files:**
- Create: `hack/grafana.sh`
- Modify: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Consumes: the pod annotations from Task 22.
- Produces: `make grafana`, and nothing other tasks read.

Not `kube-prometheus-stack`: it installs an operator, its CRDs, node-exporter, kube-state-metrics and a default alert set in order to scrape six series — more moving parts than the thing being observed. The two charts alone are a Prometheus and a Grafana.

- [ ] **Step 1: Write the script**

Create `hack/grafana.sh`:

```bash
#!/usr/bin/env bash
# Installs a Prometheus and a Grafana into the kind cluster and points Grafana
# at Prometheus, so the six series in pkg/metrics can be looked at.
#
# The raw charts, not kube-prometheus-stack: see specs/003-bonus/spec.md. No
# operator, no CRDs, no ServiceMonitor — the controller's pod carries
# prometheus.io/scrape annotations and the chart's default kubernetes_sd
# config picks them up.
#
# No dashboards are checked in. One that drifts from the metric names is worse
# than none, and the queries below are in this script where they are read.
set -euo pipefail

ns=monitoring

need() { command -v "$1" >/dev/null || { echo "missing: $1"; exit 1; }; }
need helm
need kubectl

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null
helm repo add grafana https://grafana.github.io/helm-charts >/dev/null
helm repo update >/dev/null

kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# Server only. alertmanager, pushgateway and the exporters have nothing to do
# with six counters on one pod.
helm upgrade --install prometheus prometheus-community/prometheus \
  --namespace "$ns" --wait --timeout 5m \
  --set alertmanager.enabled=false \
  --set prometheus-pushgateway.enabled=false \
  --set prometheus-node-exporter.enabled=false \
  --set kube-state-metrics.enabled=false \
  --set server.persistentVolume.enabled=false \
  --set server.global.scrape_interval=15s >/dev/null

helm upgrade --install grafana grafana/grafana \
  --namespace "$ns" --wait --timeout 5m \
  --set persistence.enabled=false \
  --set adminPassword=admin \
  --set 'datasources.datasources\.yaml.apiVersion=1' \
  --set 'datasources.datasources\.yaml.datasources[0].name=Prometheus' \
  --set 'datasources.datasources\.yaml.datasources[0].type=prometheus' \
  --set 'datasources.datasources\.yaml.datasources[0].url=http://prometheus-server.monitoring.svc' \
  --set 'datasources.datasources\.yaml.datasources[0].access=proxy' \
  --set 'datasources.datasources\.yaml.datasources[0].isDefault=true' >/dev/null

# Review Focus 5: a Prometheus that scrapes nothing and a quiet controller are
# the same empty panel. Fail here instead, where the cause is visible.
echo "== waiting for the controller target to be scraped =="
for i in $(seq 1 30); do
  up=$(kubectl -n "$ns" exec deploy/prometheus-server -c prometheus-server -- \
    wget -qO- 'http://localhost:9090/api/v1/query?query=up{app="network-isolation"}' 2>/dev/null || true)
  if printf '%s' "$up" | grep -q '"value"'; then
    echo "ok    prometheus is scraping the controller"
    break
  fi
  [ "$i" -eq 30 ] && { echo "FAIL  prometheus scraped no controller target after 60s"; exit 1; }
  sleep 2
done

cat <<'EOF'

== ready ==
  kubectl -n monitoring port-forward svc/grafana 3000:80
  open http://localhost:3000    (admin / admin)

Queries worth a panel:
  sum by (phase) (hardening_reconcile_total)
  rate(hardening_targets_patched_total[5m])
  rate(hardening_targets_reverted_total[5m])
  hardening_queue_depth
  rate(hardening_dryrun_refusals_total[5m])
  rate(hardening_apply_failures_total[5m])
EOF
```

```bash
chmod +x hack/grafana.sh
```

- [ ] **Step 2: Add the make target**

Add to `.PHONY` and to the `Makefile`:

```make
# Not part of verify: it installs two Helm charts and is for looking at the
# numbers by hand, not for asserting anything in CI.
grafana:
	./hack/grafana.sh
```

- [ ] **Step 3: Run it against a live kind cluster**

```bash
make kind-up || true
make deploy
make grafana
```

Expected: `ok    prometheus is scraping the controller`, then the ready banner. If the scrape check times out, `kubectl -n monitoring exec deploy/prometheus-server -c prometheus-server -- wget -qO- localhost:9090/api/v1/targets` shows why — most likely the annotations in Task 22 Step 1 did not land on the **pod template**, only on the Deployment.

- [ ] **Step 4: Confirm a query returns data**

```bash
kubectl -n monitoring exec deploy/prometheus-server -c prometheus-server -- \
  wget -qO- 'http://localhost:9090/api/v1/query?query=hardening_reconcile_total' | head -c 400
```

Expected: a JSON body with `"status":"success"` and at least one entry in `result`.

- [ ] **Step 5: Document it in the README**

Under `## Tests` (or a new `## Metrics` section if Part A's README merge has happened), add:

```markdown
### Metrics

The controller serves six series on `:8080/metrics` — reconciles by resource
and phase, targets patched, targets reverted, dry-run refusals, apply failures
and queue depth.

`make grafana` installs a Prometheus and a Grafana into the kind cluster and
wires them together. It is not part of `make verify`: it is for looking at the
numbers, not for asserting them. CI asserts the endpoint instead.
```

- [ ] **Step 6: Commit**

```bash
git add hack/grafana.sh Makefile README.md
git commit -m "feat(metrics): make grafana, from the raw charts

Not kube-prometheus-stack: an operator, its CRDs, node-exporter,
kube-state-metrics and a default alert set, to scrape six series on one pod.
The two charts alone are a Prometheus and a Grafana, with the exporters and
alertmanager turned off.

No dashboards checked in — one that drifts from the metric names is worse than
none, so the queries are in the script, where they are read. The script fails
if Prometheus has not scraped the controller within 60s, because a scrape that
silently matched nothing and a quiet controller are the same empty panel.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Acceptance criteria coverage

Every criterion in `specs/003-bonus/spec.md`, and the task whose tests carry it. The Refactor, Metrics endpoint and Grafana sections of the spec name no acceptance criteria; their tasks are covered by the Review Focus table below and by the suite that already passes.

| ID | Task | Evidence |
| --- | --- | --- |
| AC-U01 | 15 | `TestUndoRevertsEverythingItWrote` — unit |
| AC-U02 | 8, 9 | `TestReadLeafRendersExactlyWhatBuildWrote`, `TestInvertLeavesHumanEditsAlone` — unit |
| AC-U03 | 9, 18 | `TestInvertSeccompDeletesTheParent` (the path) — unit; the real dry-run — script |
| AC-U04 | 18 | `hack/verify-undo.sh`, `undo-restricted` and `undo-baseline` — script |
| AC-U05 | 9, 18 | `TestInvertLimitRangeGate` — unit; `hack/verify-undo.sh`, `undo-open` — script |
| AC-U06 | 15 | `TestUndoPartialRewritesTheAnnotation` — unit |
| AC-U07 | 14 | `TestUndoRejectsAProtectedNamespace`, `TestUndoRefusesTargetsThatWillNotRoll` — unit; the skip clause at Task 12 `TestDiscoverHonourSkipIsOptional` |
| AC-U08 | 18 | `hack/verify-undo.sh` — script. A fake client does not run the Deployment controller, so the annotation is never copied onto a ReplicaSet at all |
| AC-U09 | 15 | `TestUndoPreviewWritesNoDeletion` — unit |
| AC-U10 | 15 | `TestUndoRecreatedWorkloadHasNothingToRevert` — unit |
| AC-U11 | 18 | `hack/verify-undo.sh`, the full harden-approve-apply-undo-approve-apply cycle — script |
| AC-U12 | 11 | `hack/verify-crd-undo.sh`, fifteen cases — script |
| AC-U13 | 16 | Added in Task 16, Step 1 on `TestHardeningAndUndoConverge`'s fixture — unit. See Deviations item 4 |
| AC-U14 | 16 | `TestHoldExcludesTheTargetFromHardening` — unit |
| AC-U15 | 16 | `TestDeleteReleasesOnlyWhatItWrote` — unit |
| AC-U16 | 16 | `TestHardeningAndUndoConverge` — unit |
| AC-U17 | 15 | `TestUndoPartialRewritesTheAnnotation`'s closing assertion — unit |
| AC-U18 | 13, 14 | `TestFirstConflict`, `TestUndoExclusivity` — unit |
| AC-U19 | 12, 14 | `TestDiscoverSelectorMatchesTheWorkloadsOwnLabels`, `TestDiscoverNilSelectorMatchesEverything`, `TestTwoUndosWithDisjointSelectorsBothProceed` — unit |
| AC-U20 | 13, 16 | `TestReleaseFor`, `TestDeleteReleasesOnlyWhatItWrote` — unit |
| AC-U21 | 16 | `TestUnarmedUndoHoldsWithoutReverting` — unit |
| AC-U22 | 16 | `TestHoldIsReassertedAndOtherwiseSilent` — unit |

## Review Focus coverage

| # | Condition | Task, Step |
| --- | --- | --- |
| 1 | `[ALL]` rendering | 8 Step 1, 9 Step 2 |
| 2 | A removal that passes the dry-run and halts the rollout | 9 Step 2, 18 Step 5 |
| 3 | A hold that silently lapses | 16 Step 1 |
| 4 | A malformed provenance annotation | 9 Step 2, 15 Step 1 |
| 5 | An orphaned `skip-by` | 13 Step 1, 14 Step 1 |
| 6 | A path naming a missing container or the wrong list | 8 Step 1 |
| 7 | A hold patch that reaches `spec` | 10 Step 1, 16 Step 1 |
| 8 | Two hardening objects in sequence | 9 Step 2 |
| 9 | An unbounded metric label | 19 Step 2 |
| 10 | A metrics server that ignores `SIGTERM` | 21 Step 4 |
| 11 | A Prometheus that scrapes nothing | 23 Step 1 |
| 12 | A wire format broken by a type rename | 1 Step 2 |
| 13 | A coverage gate that cannot fail | 4 Step 5 |
| 14 | A finding order that moves between passes | 7 Step 1 |
| 15 | A CI cluster name that disagrees with the Makefile | 5 Step 2 |

## Deviations and clarifications to confirm before merging

Per `AGENTS.md`, the Software Engineer may not invent requirements. Each item below is a point where the spec is silent, admits two readings, or asks for something the plan declines; the plan states which reading it takes and why, rather than resolving it quietly. Items 1–3 need a spec amendment or the user's assent before Part B merges. Items 4–6 are gaps this plan closes on its own authority and flags. Items 7–8 decline spec items outright and are the two that most need review. Items 9–11 are facts about the tooling rather than open questions.

1. **`plan.Invert` returns `(Plan, string)`, not `Plan`.** FR-U02 gives the signature as returning `Plan`. BR-U05 needs the records that **survived**, to rewrite the annotation in the same request — and `Plan` carries only `Changes` and `Findings`, while `plan.Finding` has no path field, so the survivors cannot be recovered from it. The second result is the rewritten annotation value, `""` meaning remove the key. The alternative is for the controller to reverse-map deletion paths back to records through BR-U03's table, which is `Invert`'s job and belongs beside it. Implemented at Task 9; nothing else about FR-U02 changes.

2. **A per-target outcome the spec does not name: `Held`.** FR-U06 replaces `Patched` with `Reverted` and says nothing about a workload that is selected but carries no record. AC-U21 requires exactly such a workload to be visibly held, which needs a status row, which needs an outcome — and `Outcome` carries no `omitempty`, so leaving it blank would serialise as an empty string and read as a fault, which is 002's Deviation 2 repeated. `Held` is that outcome. The CRD declares `outcome` as a free string, so this costs vocabulary and no schema change. Implemented at Task 11.

3. **FR-U05's "FR-05 unchanged" cannot be taken literally, and the plan does not.** FR-05 returns from an `Applied` object before it reads anything; BR-U09 requires the hold re-asserted on every resync; AC-U22 tests it. The plan makes `Applied` terminal for the **revert** — the deletion plan is not recomputed and no `spec.template` is touched again until `approvedPlan` moves — while the hold comparison runs on every pass. This is the spec's own position in BR-U09's prose and its FR-U05 text has been amended to match; the item is recorded here because it is the one structural difference between this reconciler and 002's, and a reader who skims will assume it is a copy. Implemented at Task 16, commented at the guard.

4. **AC-U13 had no test in any task, and one is added.** Caught in self-review: the criterion — "after a full undo, an `Applied` WorkloadHardening whose generation has not moved issues no writes; editing its `approvedPlan` republishes the undone target with a new hash as `Unapproved`, and does not patch it" — fell between Task 15 (the revert) and Task 16 (the hold). It is added to Task 16, Step 1, on `TestHardeningAndUndoConverge`'s fixture. Worth noting the outcome is **stronger** than the criterion asks: once the hold is written, the reverted target is excluded by BR-04 and becomes a *finding*, not an `Unapproved` row, so it is never republished at all. The test asserts the stronger property and says why.

5. **`deploy/crd-hardening.yaml`'s description is corrected in Part B, not in Part A's docs pass.** The spec's Refactor section puts "There is no undo." in the docs pass. That sentence is still **true** until the undo CRD exists, so correcting it in Task 6 would make the repository briefly wrong in the other direction. It moves to Task 11, the commit that makes it false. Every commit's operator-facing text is then accurate.

6. **`workloadTarget` gains an `Annotations` field.** Not named anywhere in the spec. The hold decision (BR-U09), the release (BR-U10) and the exclusivity check (BR-U11) all read three annotations off each selected workload, and discovery has the object in hand. Carrying the map out of discovery makes all three cost zero extra reads; the alternative is a second `Get` per target per pass, on a code path BR-U09 already prices in `List`s. Implemented at Task 12.

7. **DECLINED — envtest for the CEL rules and structural schemas.** The spec's integration-tests section asks for it as item 3, on the ground that the scripts "cover only as far as `kubectl apply` reports". They cover considerably more: `hack/verify-crd-hardening.sh` runs fourteen `expect_reject`/`expect_accept` cases against a real API server and asserts a specific **error substring** for each, with a comment explaining that without the substring "a manifest that fails to parse, or one rejected for an unrelated reason, reads as a passing test". That is the assertion envtest would make, against the same admission chain, on a cluster CI now starts anyway (Task 5). Buying it would mean `sigs.k8s.io/controller-runtime` plus a downloaded control-plane binary — a large new dependency against NFR-01, whose single waiver is `prometheus/client_golang` and is granted for a different reason. `hack/verify-crd-undo.sh` (Task 11) extends the existing mechanism rather than introducing a second one. **If the spec wants envtest regardless, say so and NFR-01 needs a second waiver.**

8. **DECLINED for now — consolidating `hardening_test.go`'s tables.** The spec's Architecture paragraph bounds it at "one pass for table consolidation and no more". It is the one change in Part A where the "existing tests are the proof" rule cannot apply, because the tests *are* what is being changed — 1407 lines of passing assertions reorganised for shape, with nothing to verify the result against beyond a still-green suite that would stay green if a case were dropped. The honest reading of the spec's own bound is not this week. Revisit when a fourth feature needs to add cases to that file and the shape actively obstructs it. **The `discover` split, the other half of that paragraph, is not declined** — it is Task 7, and it carries a characterisation test for finding order.

9. **`fake.Clientset` does not honour `DryRun`, and validates no API type.** Verified against `k8s.io/client-go@v0.37.1`; 002 found the first half and installs the `dryRunGuard` reactor for it, which Part B reuses unchanged. The second half is what moves AC-U03's dry-run clause from unit to script (Task 18): a fake client would accept a `seccompProfile: {}` patch that a real API server refuses on every target, and the criterion the spec calls one of the two most easily got wrong would pass green all the way to a cluster.

10. **Neither fake bumps `metadata.generation`.** 002's Deviation 12, inherited. FR-U05's revert-terminality gate compares `generation` against `status.observedGeneration`, so the `arm` helper at Task 15 increments it explicitly, with a comment saying why. Without that, no test could exercise a re-approval.

11. **client-go's warning handler is per-`rest.Config`, not per-request.** FR-U03 says dry-run warnings are "captured through the client's warning handler" and reported as findings on that target. There is no per-call API on a typed clientset, and one clientset serves every reconciler, so the capture is a sink the handler appends to and the caller drains around each patch. That is correct only because `controller.go` runs a single worker — recorded here because it is what breaks first if a second worker ever looks attractive, and it breaks by attributing one workload's warning to another rather than by failing.
