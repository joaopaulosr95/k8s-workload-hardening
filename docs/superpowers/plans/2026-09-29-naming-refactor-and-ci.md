# Naming Refactor and CI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make 001's identifiers, make targets and docs as explicit as 002's, and put the whole existing verification suite under CI.

**Architecture:** Pure renames plus one function extraction — no behaviour changes anywhere, so the 7687 lines of existing tests are the proof rather than something to rewrite. Then a two-job GitHub Actions workflow: unit tests with a coverage floor, and a kind cluster running every `hack/verify-*.sh` script that exists today.

**Tech Stack:** Go 1.27.1 (vendored, `k8s.io/*` v0.37.1), GNU make, bash, kind, GitHub Actions.

**Spec:** `specs/003-bonus/spec.md` — sections "Refactor" and "Integration/e2e tests on kind".

## Global Constraints

- **No new dependencies.** NFR-01 of `specs/001-network-isolation/spec.md`. The one waiver in this project is `prometheus/client_golang`, and it belongs to a different plan. `go.mod` must be byte-identical before and after this plan.
- **No behaviour changes.** From the spec: "No behaviour changes, so the existing tests are the proof. Anything that needs a test edit beyond a rename is a finding, not a refactor."
- **The repo vendors.** `vendor/` is committed; `go build`/`go test` use `-mod=vendor` implicitly. Never run `go mod tidy` in this plan.
- **Go 1.27.1**, as pinned in `go.mod`.
- **90% unit coverage per `AGENTS.md`.** Today's total is 93.5% and no package is below 91.7%.
- **Annotation keys and CRD field names are on-disk in live clusters and are NOT renamed by this plan:** `hardening.acme.corp/filled`, `hardening.acme.corp/skip`, `hardening.acme.corp/owner`, `hardening.acme.corp/operation`, `hardening.acme.corp/cleanup`, and every `json:` tag.
- **Deliberately NOT renamed:** `deployment/network-isolation` and the `isolation-system` namespace. The spec calls this "a decision rather than a rename — it moves an RBAC subject and every script that names it — and skipping it costs nothing but a misleading name." Skipped.

## Review Focus

1. **A NetworkIsolation stored by the old binary must still round-trip after the type rename.** The wire format is the `json:` tags, not the Go type names — but a careless sed can hit a tag. Pinned in Task 1.
2. **`make verify` must build and load the image exactly once** even though both `verify-isolation` and `verify-hardening` depend on `deploy`. Pinned in Task 4.
3. **CI's kind cluster name must equal the Makefile's `CLUSTER`**, or `kind load docker-image` pushes the image into a cluster `kubectl` is not pointing at and every pod stays `ErrImageNeverPull`. Pinned in Task 5.
4. **The coverage gate must fail the build, not just print.** A `| tail -1` that reports 42% and exits 0 is worse than no gate. Pinned in Task 5.
5. **The `discover` split must preserve finding order.** Findings are sorted and `setHardeningStatus` writes only on a semantic difference; a reordering would make every resync of every object write status forever. Pinned in Task 7.

---

### Task 1: Rename the isolation API types

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

### Task 2: Rename the isolation reconciler

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

### Task 3: Rename plan.Policy to plan.Request

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

### Task 4: Symmetric make targets

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

### Task 5: CI

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `make test`, `make cover`, `make verify`, `make verify-crd` from Task 4.
- Produces: a workflow named `ci` with jobs `unit` and `kind`. The undo plan appends two steps to the `kind` job.

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

### Task 6: Merge the README

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

### Task 7: Split the reporting half out of discover

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

## Declined from the spec, with reasons

**envtest for the CEL rules and structural schemas** (spec, "Integration/e2e tests on kind", item 3). Declined.

It needs `sigs.k8s.io/controller-runtime/pkg/envtest` plus a downloaded control-plane binary. controller-runtime is a large new dependency and NFR-01 forbids new dependencies — the project's one waiver is `prometheus/client_golang`, granted explicitly and for a different reason.

What it would buy is already bought. `hack/verify-crd-hardening.sh` runs fourteen `expect_reject`/`expect_accept` cases against a real API server and asserts a specific error substring for each one, with a comment explaining why the substring is load-bearing: "without it a manifest that fails to parse, or one rejected for an unrelated reason, reads as a passing test." That is not "as far as `kubectl apply` reports" — it is the same assertion envtest would make, against the same admission chain, on a cluster CI now starts anyway (Task 5). `hack/verify-crd-isolation.sh` does the same for 001.

The undo plan extends this with `hack/verify-crd-undo.sh` rather than introducing a second mechanism.

**Consolidating `hardening_test.go`'s tables** (spec, "Architecture"). Declined for now.

1407 lines of passing tests reorganised for shape is churn with no behaviour to verify the result against — the one refactor in this plan whose "existing tests are the proof" rule cannot apply, because the tests *are* the thing being changed. The spec already bounds it at "one pass and no more"; the honest version of that bound is not this week. Revisit if a third feature needs to add cases to the same file and the shape actively obstructs it.

---

## Self-Review

**Spec coverage.** Refactor → Go renames Tasks 1–3, build and manifests Task 4, docs Task 6, architecture Task 7. Integration/e2e item 1 (CI) → Task 5. Items 2 (`verify-undo.sh`) and 3 (envtest) → the undo plan and Declined respectively. The `deployment/network-isolation` rename is explicitly skipped per the spec's own reasoning, recorded in Global Constraints. `deploy/crd-hardening.yaml`'s "There is no undo." description is **not** touched here: it is still true until the undo CRD exists, so it moves to the undo plan's CRD task, where changing it keeps every commit's docs accurate.

**Placeholders.** None. Every step names its command and its expected output. Step 1 of Task 6 gives the target structure rather than the final prose, which is the one place the deliverable is editorial judgement over a file the executor can read — the three verification steps that follow it are mechanical.

**Type consistency.** `IsolationSpec`/`IsolationStatus` (Task 1) are consumed by Task 2. `IsolationReconciler` and `setStatus` (Task 2) appear nowhere later. `plan.Request` (Task 3) is independent. `discoverFindings` (Task 7) is new and has no consumer. Make targets `verify`/`verify-crd` (Task 4) are consumed by Task 5 and by README checks in Task 6.

**Review Focus.** Five, each with a test in the owning task: wire format → Task 1 Step 2; single deploy → Task 4 Step 4; cluster name → Task 5 Step 2; coverage gate fails → Task 4 Step 5; finding order → Task 7 Step 1.
