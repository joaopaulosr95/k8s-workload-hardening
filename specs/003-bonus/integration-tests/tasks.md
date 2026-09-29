# Integration and e2e Tests on kind — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the verification that already exists under CI, and add the two scripts that cover what a fake client cannot reach.

**Architecture:** Most of this exists. `hack/verify-isolation.sh` already runs the full NetworkIsolation cycle on a live cluster, and `hack/verify-hardening.sh` and `hack/verify-crd-hardening.sh` already cover AC-15 and AC-16. The gap is that **nothing runs them**: `.github/workflows/` exists and is empty. Task 1 is the whole of the bonus for 001 and 002. Task 2 brings all three CRDs' schemas and CEL rules under **envtest**, in the fast job and with no cluster — and replaces the third `verify-crd` script rather than joining it. Task 3 extends the existing script mechanism to the undo's five criteria a fake client cannot reach.

**Tech Stack:** GitHub Actions · kind v0.32.0 · kubectl v1.36.2 · bash · GNU make · Go 1.27.1 for the unit job.

**Spec:** `specs/003-bonus/spec.md`, the **Integration/e2e tests on kind** section.

**Depends on:** the refactor plan's Task 4 for the `verify` and `verify-crd` aggregates, and the undo plan for everything Tasks 2 and 3 verify. Rewriting the existing scripts from a fresh test plan would rebuild working code, and this plan does not.

---

## Global Constraints

- **NFR-01 is waived a second time, for `sigs.k8s.io/controller-runtime`, and for nothing else.** The spec grants it in its integration-tests section. **After adding it, run `go mod vendor` so the local build keeps working, and commit `go.mod` and `go.sum` — `vendor/` is not tracked.** `setup-envtest` is a build tool run through `go run` at a pinned version: it is never imported, so it does not enter `go.mod` either.
- **Both the tool version and the control-plane version are pinned** in the `Makefile`. `setup-envtest` at HEAD would change the API server under CI without a commit, and a schema that passes on one version and fails on the next is exactly what this suite exists to catch.
- **The existing scripts are not rewritten.** `hack/verify-isolation.sh`, `hack/verify-hardening.sh` and `hack/verify-crd-hardening.sh` are finished. The only change any of them may receive is a rename, and that belongs to the refactor plan.
- **Every `expect_reject` asserts a specific error substring.** `hack/verify-crd-hardening.sh` carries the reason in a comment: "without it a manifest that fails to parse, or one rejected for an unrelated reason, reads as a passing test." Task 2 follows it.
- **The CI cluster name must equal the Makefile's `CLUSTER`.** `kind load docker-image` names the cluster explicitly.
- **Failure must be diagnosable from the job log alone.** A red kind job with no controller logs costs a full re-run to understand.
- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1, exactly as `go.mod` declares.
- **`vendor/` is on disk but is NOT committed.** It is listed in `.gitignore` and `git ls-files vendor` returns nothing. Two consequences, and they pull in opposite directions. Locally, `vendor/modules.txt` exists, so Go builds in vendor mode automatically — verified: `go list` resolves `k8s.io/client-go` to `./vendor/...` — and a newly added dependency makes the build fail with "inconsistent vendoring" until `go mod vendor` is re-run. In a fresh clone, which is what CI gets, there is no `vendor/` at all and modules resolve from the cache against `go.sum`. So: run `go mod vendor` locally to keep building, and commit **`go.mod` and `go.sum` only**. Never run `go mod tidy`.
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (`AGENTS.md`), enforced by `make cover`. Today's total is 93.5% and the lowest package is 91.7%. `cmd/` is wiring and is excluded.
- **`AGENTS.md` role constraint:** do not edit `specs/003-bonus/spec.md`, and do not create or modify anything else under `specs/` except this file. If the implementation needs behaviour the spec does not describe, stop and raise it rather than inventing a requirement. Every such point this plan already found is listed in "Deviations and clarifications to confirm before merging" at the end.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `refactor:`, `docs:`, `build:`, `ci:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Four conditions, ordered by how likely each is to waste someone's afternoon.

1. **A CI cluster name that disagrees with the Makefile's.** `kind load docker-image` names the cluster explicitly; a mismatch loads the image into a cluster `kubectl` is not pointing at, and every pod sits in `ErrImageNeverPull` with no error naming the cause. Expected: the two strings are compared in the job that depends on them. → **Task 1, Step 2.**
2. **A green script that tested nothing.** `hack/verify-undo.sh` passes trivially if the namespaces it needs do not produce the admission conditions it is checking — a missing `enforce` label, or a LimitRange that happens to carry a default. Expected: the run asserts that at least one `BlockedByPodSecurity` and one `BlockedByLimitRange` finding actually appeared. → **Task 3, Step 5.**
3. **A transition rule that rejects everything rather than only what it should.** An unguarded copy of 002's CEL rule raises a runtime error on a `WorkloadHardeningUndo` with no `workloadSelector`, and an erroring transition rule rejects the update — including the `approvedPlan` edit that arms the object. That reads as "the CRD is broken", not as "the rule is wrong". This is the failure the envtest dependency was waived for, so the task reproduces it deliberately before trusting the suite. → **Task 2, Steps 4 and 7.**
4. **An envtest suite that skipped rather than passed.** It exits 0 when the control-plane binaries are absent, so `go test ./...` works on a bare machine — and a failed asset fetch in CI then produces the same summary line as a clean run. Expected: CI greps for the skip message and for the `ok` line, and fails on either being wrong. → **Task 2, Steps 7 and 8.**

---

## File Structure

| File | Responsibility |
| --- | --- |
| `.github/workflows/ci.yml` | **New.** Two jobs: `go vet` plus the unit suite behind the coverage floor, and a kind cluster running `make verify-crd` and `make verify`. Controller logs dumped on failure. |
| `test/envtest/suite_test.go` | **New.** One control plane for the package; installs the three CRDs from `deploy/`, so the manifests under test are the ones shipped. |
| `test/envtest/crd_test.go` | **New.** AC-U12 in full, plus every case ported from the two existing `verify-crd` scripts. Each rejection asserts a specific error substring. |
| `hack/verify-undo.sh` | **New.** AC-U03's dry-run half, AC-U04, AC-U05, AC-U08 and AC-U11 on a live cluster. |
| `deploy/samples/workloads-undo.yaml` | **New.** Three namespaces producing both of BR-U04's gates plus a plain one, and the four Deployments they need. |
| `Makefile` | **Modified.** `envtest-assets` and `test-envtest` with both versions pinned; `samples-undo` and `verify-undo` added to the aggregates. |
| `.gitignore` | **Modified.** `bin/` — the control-plane binaries are fetched, not committed. |

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
          # A fresh clone has no vendor/ — it is gitignored — so every run
          # resolves modules from go.sum. Caching is worth having here, and
          # the k8s.io tree is most of what it saves.
          cache: true
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
          cache: true
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

### Task 2: envtest for all three CRD schemas and CEL rules (G-06 item 3; AC-U12)

**Files:**
- Create: `test/envtest/suite_test.go`
- Create: `test/envtest/crd_test.go`
- Modify: `go.mod`, `go.sum`, `.gitignore`, `Makefile`, `.github/workflows/ci.yml` (`vendor/` is regenerated locally and is not tracked)

**Interfaces:**
- Consumes: `deploy/crd-isolation.yaml`, `deploy/crd-hardening.yaml` (both shipped), and `deploy/crd-undo.yaml` from the undo plan's Task 4.
- Produces: `make envtest-assets`, `make test-envtest`, and a step in Task 1's `unit` job. Nothing else reads them.

This **replaces** the third CRD script. One mechanism per question: `hack/verify-crd-isolation.sh` and `hack/verify-crd-hardening.sh` stay because they are written, passing, and assert the installed-and-served path rather than the schema — but their **cases** are ported here, and no `hack/verify-crd-undo.sh` is written.

NFR-01 is waived a second time for this, on the spec's authority. The argument is specific rather than general: a CEL transition rule that *errors* rather than refuses — which is exactly what an unguarded copy of 002's rule does against an optional `workloadSelector` — rejects every update including the one that arms the object, and reads as a broken CRD rather than a wrong rule. A Go test on every pull request is a better place to catch that than a cluster job.

- [ ] **Step 1: Add the dependency and vendor it**

```bash
go get sigs.k8s.io/controller-runtime@latest
go mod vendor      # local only: vendor/ is gitignored, and without this the
                   # build fails with "inconsistent vendoring"
go build ./... && echo BUILD-OK
git diff --stat go.mod go.sum      # this is what the commit carries
```

Expected: `BUILD-OK`, and a `go.mod`/`go.sum` diff naming controller-runtime and its transitive set.

`setup-envtest` is a **build tool, not a dependency**: it is run through `go run` at a pinned version below and is never imported, so it does not enter `go.mod` and is not vendored.

- [ ] **Step 2: Add the asset and test targets**

In the `Makefile`:

```make
# Pinned, both of them. setup-envtest at HEAD would change the control-plane
# version under CI without a commit, and a schema that passes on one API server
# version and fails on the next is exactly what this suite exists to catch.
ENVTEST_VERSION ?= release-0.22
ENVTEST_K8S_VERSION ?= 1.33.0
SETUP_ENVTEST = go run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

envtest-assets:
	@$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(CURDIR)/bin -p path

test-envtest:
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(CURDIR)/bin -p path)" \
		go test ./test/envtest/... -count=1
```

Add both to `.PHONY`, and `bin/` to `.gitignore` — the control-plane binaries are fetched, not committed.

- [ ] **Step 3: Write the harness**

Create `test/envtest/suite_test.go`:

```go
// Package envtest_test runs the three CRDs this repository ships against a real
// API server and etcd, with no kubelet and no nodes.
//
// It is the only place the structural schemas and the CEL transition rules are
// exercised from Go. The hack/verify-crd-*.sh scripts assert the same things
// through kubectl against a kind cluster; those stay, because they also prove
// the manifests install through `make deploy`. This asserts the schema itself,
// on every pull request, in the fast job.
package envtest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var dyn dynamic.Interface

const group = "hardening.acme.corp"

var (
	isolationGVR = schema.GroupVersionResource{Group: group, Version: "v1alpha1", Resource: "networkisolations"}
	hardeningGVR = schema.GroupVersionResource{Group: group, Version: "v1alpha1", Resource: "workloadhardenings"}
	undoGVR      = schema.GroupVersionResource{Group: group, Version: "v1alpha1", Resource: "workloadhardeningundos"}
)

// TestMain starts one control plane for the whole package and installs the
// three CRDs from deploy/, so the manifests under test are the ones shipped
// rather than a copy that can drift.
//
// It exits 0 with a message when KUBEBUILDER_ASSETS is unset, so `go test ./...`
// on a machine with no control-plane binaries does not fail. That is also the
// one way this suite can lie — a skip and a pass are indistinguishable in a
// summary line — which is why Step 7 makes CI assert that it actually ran.
func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Println("envtest: KUBEBUILDER_ASSETS is unset; run `make envtest-assets`. Skipping.")
		os.Exit(0)
	}

	env := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				filepath.Join("..", "..", "deploy", "crd-isolation.yaml"),
				filepath.Join("..", "..", "deploy", "crd-hardening.yaml"),
				filepath.Join("..", "..", "deploy", "crd-undo.yaml"),
			},
			ErrorIfPathMissing: true,
		},
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	if dyn, err = dynamic.NewForConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "dynamic client: %v\n", err)
		_ = env.Stop()
		os.Exit(1)
	}

	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
	}
	os.Exit(code)
}

// apply creates obj, or updates it if it is already stored. Updating rather
// than recreating is what exercises a CEL transition rule at all: oldSelf only
// exists on an update.
func apply(gvr schema.GroupVersionResource, obj map[string]any) error {
	u := &unstructured.Unstructured{Object: obj}
	c := dyn.Resource(gvr).Namespace(u.GetNamespace())
	if stored, err := c.Get(context.Background(), u.GetName(), metav1.GetOptions{}); err == nil {
		u.SetResourceVersion(stored.GetResourceVersion())
		_, err := c.Update(context.Background(), u, metav1.UpdateOptions{})
		return err
	}
	_, err := c.Create(context.Background(), u, metav1.CreateOptions{})
	return err
}

// rejects asserts the API server refuses obj, and refuses it for the stated
// reason. The expected substring is not decoration: without it, an object
// rejected for an unrelated reason — or one that failed to parse — reads as a
// passing test. hack/verify-crd-hardening.sh already carries that rule in a
// comment; this is the same rule in Go.
func rejects(t *testing.T, gvr schema.GroupVersionResource, want string, obj map[string]any) {
	t.Helper()
	err := apply(gvr, obj)
	if err == nil {
		t.Fatalf("accepted; want rejected with %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("rejected for the wrong reason:\n  got:  %v\n  want: %q", err, want)
	}
}

func accepts(t *testing.T, gvr schema.GroupVersionResource, obj map[string]any) {
	t.Helper()
	if err := apply(gvr, obj); err != nil {
		t.Fatalf("rejected; want accepted: %v", err)
	}
}

// object is the boilerplate every case shares.
func object(kind, name string, spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": group + "/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": "default"},
		"spec":       spec,
	}
}
```

- [ ] **Step 4: Write the undo CRD's cases**

Create `test/envtest/crd_test.go`. The undo CRD is new, so its cases are written here in full; the other two are ported in Step 5.

```go
package envtest_test

import "testing"

func undoSpec(extra map[string]any) map[string]any {
	spec := map[string]any{"namespaces": []any{"tenant-a"}}
	for k, v := range extra {
		spec[k] = v
	}
	return spec
}

// AC-U12's structural half. Every one of these is a shape the API server must
// refuse before an object reaches the controller at all.
func TestUndoSchema(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		spec       map[string]any
	}{
		{"empty namespace list", "should have at least 1 items",
			map[string]any{"namespaces": []any{}}},
		{"seventeen namespaces", "must have at most 16 items",
			map[string]any{"namespaces": []any{"n01", "n02", "n03", "n04", "n05", "n06", "n07", "n08", "n09", "n10", "n11", "n12", "n13", "n14", "n15", "n16", "n17"}}},
		{"duplicate namespace", "Duplicate value",
			map[string]any{"namespaces": []any{"tenant-a", "tenant-a"}}},
		{"namespaces omitted", "spec.namespaces: Required value",
			map[string]any{}},
		{"an uppercase namespace", "in body should match",
			map[string]any{"namespaces": []any{"Tenant-A"}}},
		{"a matchExpressions selector", "matchExpressions",
			undoSpec(map[string]any{"workloadSelector": map[string]any{
				"matchExpressions": []any{map[string]any{"key": "app", "operator": "Exists"}}}})},
		{"an empty matchLabels", "should have at least 1 properties",
			undoSpec(map[string]any{"workloadSelector": map[string]any{"matchLabels": map[string]any{}}})},
		{"nine matchLabels entries", "must have at most 8 properties",
			undoSpec(map[string]any{"workloadSelector": map[string]any{"matchLabels": map[string]any{
				"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8", "i": "9"}}})},
		{"a workloadSelector with no matchLabels", "matchLabels: Required value",
			undoSpec(map[string]any{"workloadSelector": map[string]any{}})},
		{"a thirteen-character hash", "in body should match",
			undoSpec(map[string]any{"approvedPlan": []any{"0123456789abc"}})},
		{"an uppercase hash", "in body should match",
			undoSpec(map[string]any{"approvedPlan": []any{"0123456789AB"}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejects(t, undoGVR, tc.want, object("WorkloadHardeningUndo", "schema-"+t.Name(), tc.spec))
		})
	}
}

// AC-U12's transition half, and the reason this suite is worth its dependency.
//
// These run in one test rather than as subtests because each step acts on what
// the previous one stored: a transition rule only exists on an update, so the
// order is the assertion.
//
// The two `accepts` are the load-bearing ones. An unguarded copy of 002's rule
// — `self.workloadSelector == oldSelf.workloadSelector` with no has() guard —
// raises a CEL runtime error on an object that omits the field, and a rule that
// errors rejects the update. Both of these would fail, including the one that
// arms the object, and the message would read as a broken CRD rather than a
// wrong rule.
func TestUndoTransitionRule(t *testing.T) {
	const immutable = "only spec.approvedPlan may be changed"

	// A selectorless object: created, then armed.
	accepts(t, undoGVR, object("WorkloadHardeningUndo", "no-selector",
		map[string]any{"namespaces": []any{"tenant-a", "tenant-b"}}))
	accepts(t, undoGVR, object("WorkloadHardeningUndo", "no-selector",
		map[string]any{"namespaces": []any{"tenant-a", "tenant-b"}, "approvedPlan": []any{"0123456789ab"}}))

	rejects(t, undoGVR, immutable, object("WorkloadHardeningUndo", "no-selector",
		map[string]any{"namespaces": []any{"tenant-a", "tenant-c"}, "approvedPlan": []any{"0123456789ab"}}))
	rejects(t, undoGVR, immutable, object("WorkloadHardeningUndo", "no-selector",
		map[string]any{"namespaces": []any{"tenant-a", "tenant-b"}, "approvedPlan": []any{"0123456789ab"},
			"workloadSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}}}))

	// An object that has one: created, armed, then edited and stripped.
	withSelector := func(sel map[string]any, approved ...any) map[string]any {
		spec := map[string]any{"namespaces": []any{"tenant-a"}}
		if sel != nil {
			spec["workloadSelector"] = map[string]any{"matchLabels": sel}
		}
		if len(approved) > 0 {
			spec["approvedPlan"] = approved
		}
		return object("WorkloadHardeningUndo", "with-selector", spec)
	}

	accepts(t, undoGVR, withSelector(map[string]any{"app": "api"}))
	accepts(t, undoGVR, withSelector(map[string]any{"app": "api"}, "cafebabe1234"))
	rejects(t, undoGVR, immutable, withSelector(map[string]any{"app": "worker"}, "cafebabe1234"))
	rejects(t, undoGVR, immutable, withSelector(nil, "cafebabe1234"))
}
```

- [ ] **Step 5: Port the other two CRDs' cases**

Add `TestIsolationSchema` and `TestHardeningSchema` in the same shape, porting every case from the scripts rather than inventing new ones — the scripts are the record of what these schemas are supposed to refuse.

| Source | Cases to port | Note |
| --- | --- | --- |
| `hack/verify-crd-hardening.sh` | 14 (`expect_reject` × 11, `expect_accept` × 3) | includes the `10mm` quantity case and both `limits` cases, one structural and one CEL |
| `hack/verify-crd-isolation.sh` | whatever it declares | count them first: `grep -c 'expect_reject\|expect_accept' hack/verify-crd-isolation.sh` |

Then assert the port is complete rather than trusting it:

```bash
scripts=$(grep -ch 'expect_reject\|expect_accept' hack/verify-crd-isolation.sh hack/verify-crd-hardening.sh | paste -sd+ | bc)
ported=$(grep -c '{"' test/envtest/crd_test.go)
echo "scripts: $scripts   ported (plus 15 undo cases): $ported"
```

A port that silently drops a case is the failure mode here, and it is invisible: the suite still passes.

- [ ] **Step 6: Run it**

```bash
make envtest-assets
make test-envtest
```

Expected: `ok  github.com/joaopaulosr95/k8s-workload-hardening/test/envtest`, with every subtest passing. If `TestUndoTransitionRule`'s two `accepts` fail with a CEL evaluation error rather than the immutability message, the CRD's rule is missing its `has()` guards — fix `deploy/crd-undo.yaml`, not the test.

- [ ] **Step 7: Prove the suite can fail, and cannot silently skip**

Temporarily strip the guards from `deploy/crd-undo.yaml`'s transition rule:

```yaml
                - rule: >-
                    self.namespaces == oldSelf.namespaces &&
                    self.workloadSelector == oldSelf.workloadSelector
```

```bash
make test-envtest 2>&1 | grep -c FAIL
```

Expected: non-zero, with `TestUndoTransitionRule` failing on the *first* `accepts` — the object with no selector cannot even be created's successor updated. **Restore the guards.** This is the single failure this task was waived a dependency for; a run that does not reproduce it has not earned the dependency.

Then prove a missing asset does not pass silently:

```bash
KUBEBUILDER_ASSETS= go test ./test/envtest/... 2>&1 | tail -2
```

Expected: the skip message and `ok`. That is the lie Step 8's CI step exists to catch.

- [ ] **Step 8: Wire it into CI**

In `.github/workflows/ci.yml`, in the `unit` job created by Task 1, after the coverage step:

```yaml
      - name: CRD schemas and CEL rules (envtest)
        run: |
          set -euo pipefail
          out=$(make test-envtest 2>&1) || { printf '%s\n' "$out"; exit 1; }
          printf '%s\n' "$out"
          # A skipped suite and a passing one are the same summary line, and the
          # skip is what happens when the asset fetch silently fails.
          if printf '%s' "$out" | grep -q 'KUBEBUILDER_ASSETS is unset'; then
            echo "envtest skipped: the control-plane assets were not fetched"; exit 1
          fi
          printf '%s' "$out" | grep -qE '^ok[[:space:]].*test/envtest' || { echo "envtest did not run"; exit 1; }
```

It goes in the `unit` job, not the `kind` one: envtest needs no cluster, and putting it behind the slow job would lose the whole point of adding it.

- [ ] **Step 9: Run the full suite**

Run: `go test ./... -race && make cover && make test-envtest`
Expected: every package `ok`, coverage at or above 90%, envtest green. `make cover` covers `./pkg/...` only, so `test/envtest` does not move the number — it asserts manifests, not Go statements.

- [ ] **Step 10: Commit**

```bash
git add go.mod go.sum .gitignore test/envtest Makefile .github/workflows/ci.yml
git commit -m "test(crd): all three schemas and CEL rules under envtest

NFR-01 waived a second time, for sigs.k8s.io/controller-runtime. The argument
is narrow: a CEL transition rule that errors rather than refuses rejects every
update, including the one that arms the object, and reads as a broken CRD
rather than a wrong rule. That is exactly what an unguarded copy of 002's rule
does against an optional workloadSelector, and nothing catches it until
something applies the right object in the right order.

Replaces the planned hack/verify-crd-undo.sh rather than joining it — one
mechanism per question. The two existing scripts stay: they are written,
passing, and assert the installed-and-served path through make deploy, which
this does not. Their cases are ported here rather than reinvented, and Step 5
asserts the port dropped nothing.

Runs in the unit job, not behind the kind cluster. Needing no cluster is most
of why it is worth having.

The suite exits 0 when the control-plane binaries are absent, so go test ./...
works on a bare machine — and CI therefore asserts it actually ran, because a
skip and a pass are the same summary line.

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

In `.github/workflows/ci.yml`, the `kind` job already runs `make verify`, and Step 3 above added `verify-undo` to that aggregate — so the workflow needs no edit for this task. Task 2 added its own step to the `unit` job. Confirm:

```bash
make -n verify | grep -c verify-undo.sh
grep -c 'make test-envtest' .github/workflows/ci.yml
```

Expected: `1` and `1`. Note that `verify-crd` stays a two-way aggregate: the undo CRD's schema is asserted by envtest in the fast job, not by a third script.

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
| AC-U12 | 2 | The structural schema and the CEL transition rule are evaluated by the API server; envtest gives one without a cluster |

## Review Focus coverage

| # | Condition | Task, Step |
| --- | --- | --- |
| 1 | A CI cluster name that disagrees with the Makefile | 1 Step 2 |
| 2 | A green script that tested nothing | 3 Step 5 |
| 3 | A transition rule that rejects everything | 2 Steps 4 and 7 |
| 4 | An envtest suite that skipped rather than passed | 2 Steps 7 and 8 |

## Deviations and clarifications to confirm before merging

1. **RESOLVED — envtest is built, and NFR-01 is waived a second time.** An earlier draft of this plan declined it: the `verify-crd-*.sh` scripts already assert specific error substrings against a real API server, on a cluster Task 1 now starts anyway, so envtest looked like a second mechanism for a question already answered. The decision went the other way, and the spec now records the waiver and the reason. The argument that carries it is narrower than "more testing is better": a CEL transition rule that **errors** rather than refuses rejects every update including the one that arms the object, reads as a broken CRD rather than a wrong rule, and is invisible until something applies the right object in the right order. A Go test in the fast job is where that belongs.

   Two consequences, both deliberate. Envtest **replaces** the planned `hack/verify-crd-undo.sh` rather than joining it — one mechanism per question — so `verify-crd` stays a two-way aggregate. And the two existing scripts stay exactly as they are, because they assert something envtest does not: that the manifests install through `make deploy` on a real cluster. Their cases are ported into Task 2, and Step 5 asserts the port dropped none.

2. **`hack/verify-undo.sh` lives here rather than in the undo plan.** The spec puts it here (item 2 of this section), and this plan follows it — every `hack/verify-*.sh` in the repository is owned by one document. The cost is real and worth naming: the undo plan ships a CRD whose rejection cases are asserted elsewhere, so its Task 4 proves the schema **installs** and this plan's Task 2 proves it **refuses**. If a reviewer would rather each feature carried its own verification, move Tasks 2 and 3 into the undo plan wholesale; nothing else changes.

3. **CI runs the aggregates, not the individual scripts.** Task 1's `kind` job runs `make verify` and `make verify-crd`, which the refactor plan's Task 4 defines. Tasks 2 and 3 add themselves to those aggregates rather than to the workflow, so a fourth feature's suite joins CI by editing the Makefile and nothing else. The metrics plan adds one step to this workflow directly, because a metrics assertion is not a `verify-*` script.

4. **The unit job is not gated on the kind job.** They run in parallel. A broken unit suite and a broken cluster suite are different failures and a reviewer wants both in one pass, not the second one hidden behind the first.
