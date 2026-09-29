# Integration and e2e Tests on kind — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the verification that already exists under CI, and cover the one thing no script reaches: the CRD schemas and CEL rules themselves.

**Architecture:** Most of this exists. `hack/verify-isolation.sh` already runs the full NetworkIsolation cycle on a live cluster, and `hack/verify-hardening.sh` and `hack/verify-crd-hardening.sh` already cover AC-15 and AC-16. The gap is that **nothing runs them**: `.github/workflows/` exists and is empty. Task 1 is the whole of the bonus for 001 and 002. Task 2 brings both CRDs' schemas and CEL rules under **envtest**, in the fast job and with no cluster.

**Tech Stack:** GitHub Actions · kind v0.32.0 · kubectl v1.36.2 · bash · GNU make · Go 1.27.1 for the unit job.

**Spec:** `specs/003-bonus/spec.md`, the **Integration/e2e tests on kind** section.

**Depends on:** the refactor plan's Task 4 for the `verify` and `verify-crd` aggregates. Rewriting the existing scripts from a fresh test plan would rebuild working code, and this plan does not.

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

Three conditions, ordered by how likely each is to waste someone's afternoon.

1. **A CI cluster name that disagrees with the Makefile's.** `kind load docker-image` names the cluster explicitly; a mismatch loads the image into a cluster `kubectl` is not pointing at, and every pod sits in `ErrImageNeverPull` with no error naming the cause. Expected: the two strings are compared in the job that depends on them. → **Task 1, Step 2.**
2. **A transition rule that rejects everything rather than only what it should.** A CEL transition rule that *errors* rather than refuses rejects every update — including the `approvedPlan` edit that arms the object. That reads as "the CRD is broken", not as "the rule is wrong", and it never fires on the create, where there is no `oldSelf`. This is the failure the envtest dependency was waived for, so the task reproduces it deliberately before trusting the suite. → **Task 2, Step 6.**
3. **An envtest suite that skipped rather than passed.** It exits 0 when the control-plane binaries are absent, so `go test ./...` works on a bare machine — and a failed asset fetch in CI then produces the same summary line as a clean run. Worse, `go test` suppresses a passing package's output, so grepping for the skip message cannot work. Expected: CI runs with `-v` and asserts a named test reported a result. → **Task 2, Steps 6 and 7.**

---

## File Structure

| File | Responsibility |
| --- | --- |
| `.github/workflows/ci.yml` | **New.** Two jobs: `go vet` plus the unit suite behind the coverage floor, and a kind cluster running `make verify-crd` and `make verify`. Controller logs dumped on failure. |
| `test/envtest/suite_test.go` | **New.** One control plane for the package; installs both CRDs from `deploy/`, so the manifests under test are the ones shipped. |
| `test/envtest/crd_test.go` | **New.** Every case ported from the two existing `verify-crd` scripts. Each rejection asserts a specific error substring. |
| `Makefile` | **Modified.** `envtest-assets` and `test-envtest` with both versions pinned, and `GOTESTFLAGS` so CI can ask for `-v`. |
| `.gitignore` | **Modified.** `bin/` — the control-plane binaries are fetched, not committed. |

---

### Task 1: CI on every push (G-06 item 1)

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `make test`, `make cover`, `make verify`, `make verify-crd` from the refactor plan's Task 4.
- Produces: a workflow named `ci` with jobs `unit` and `kind`. Task 2 adds one step to the `unit` job; anything later joins the `verify` and `verify-crd` aggregates this job already runs, so the workflow needs no further edit.

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
        run: kubectl -n isolation-system logs deployment/k8s-workload-hardening --tail=200 || true
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

### Task 2: envtest for both CRD schemas and CEL rules (G-06 item 3; AC-09, AC-16)

**Files:**
- Create: `test/envtest/suite_test.go`
- Create: `test/envtest/crd_test.go`
- Modify: `go.mod`, `go.sum`, `.gitignore`, `Makefile`, `.github/workflows/ci.yml` (`vendor/` is regenerated locally and is not tracked)

**Interfaces:**
- Consumes: `deploy/crd-isolation.yaml` and `deploy/crd-hardening.yaml`, both shipped.
- Produces: `make envtest-assets`, `make test-envtest`, and a step in Task 1's `unit` job. Nothing else reads them.

One mechanism per question: `hack/verify-crd-isolation.sh` and `hack/verify-crd-hardening.sh` stay because they are written, passing, and assert the installed-and-served path rather than the schema — but their **cases** are ported here.

NFR-01 is waived a second time for this, on the spec's authority. The argument is specific rather than general: a CEL transition rule that *errors* rather than refuses rejects every update including the one that arms the object, and reads as a broken CRD rather than a wrong rule. It never fires on the create, where there is no `oldSelf`, so nothing catches it until something applies the right object in the right order. A Go test on every pull request is a better place for that than a cluster job.

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
// Package envtest_test runs the two CRDs this repository ships against a real
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
)

// TestMain starts one control plane for the whole package and installs the
// two CRDs from deploy/, so the manifests under test are the ones shipped
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

- [ ] **Step 4: Port both CRDs' cases**

Add `TestIsolationSchema` and `TestHardeningSchema`, plus a sequenced transition test for each, porting every case from the scripts rather than inventing new ones — the scripts are the record of what these schemas are supposed to refuse. A transition rule only exists on an update, so the sequenced cases run in order inside one test rather than as subtests.

| Source | Cases to port | Note |
| --- | --- | --- |
| `hack/verify-crd-hardening.sh` | 15 — `expect_reject` × 13, `expect_accept` × 2 | includes the `10mm` quantity case and both `limits` cases, one structural and one CEL |
| `hack/verify-crd-isolation.sh` | 9 — `expect_reject` × 7, plus 2 accepts written inline rather than through a helper it does not define | the immutability case and the re-apply are sequenced, not standalone |

Then assert the port is complete rather than trusting it:

```bash
iso=$(( $(grep -cE '^expect_reject ' hack/verify-crd-isolation.sh) + $(grep -cE '^echo "ok    accepted:' hack/verify-crd-isolation.sh) ))
har=$(( $(grep -cE '^expect_reject ' hack/verify-crd-hardening.sh) + $(grep -cE '^expect_accept ' hack/verify-crd-hardening.sh) ))
echo "scripts: $((iso + har))"
```

Expected: `scripts: 24`, matched by 16 table rows plus 8 sequenced assertions in `crd_test.go`. Count the invocations, not every line mentioning the helper: a bare `grep -c` also counts each script's own function definition and its doc comment.

A port that silently drops a case is the failure mode here, and it is invisible: the suite still passes.

- [ ] **Step 5: Run it**

```bash
make envtest-assets
make test-envtest
```

Expected: `ok  github.com/joaopaulosr95/k8s-workload-hardening/test/envtest`, with every subtest passing.

- [ ] **Step 6: Prove the suite can fail, and cannot silently skip**

Temporarily break `deploy/crd-hardening.yaml`'s transition rule so it refuses the edit that arms an object — for instance by dropping the `approvedPlan` exemption from it — then:

```bash
make test-envtest 2>&1 | grep -c FAIL
```

Expected: non-zero, with `TestHardeningTransitionRule` failing on its second `accepts`, the one that arms an existing object. Not on the create: a transition rule has no `oldSelf` there and never runs, which is most of why this class of mistake hides. **Restore the rule.** A suite that cannot be made to fail has not earned its dependency.

Then prove a missing asset does not pass silently:

```bash
KUBEBUILDER_ASSETS= go test ./test/envtest/... -count=1 2>&1 | tail -2
```

Expected: **`ok` alone, with no skip message.** `go test` suppresses a passing package's own output, so the suite's "assets unset" line never reaches a grep while the `ok` line does. That is precisely the lie Step 7's CI step has to catch, and it is why that step asserts a named test reported a result rather than grepping for the skip message.

- [ ] **Step 7: Wire it into CI**

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

It goes in the `unit` job, not the `kind` one: envtest needs no cluster, and putting it behind the slow job would lose the whole point of adding it. `GOTESTFLAGS=-v` is not decoration — without the per-test lines there is nothing to assert on.

- [ ] **Step 8: Run the full suite**

Run: `go test ./... -race && make cover && make test-envtest`
Expected: every package `ok`, coverage at or above 90%, envtest green. `make cover` covers `./pkg/...` only, so `test/envtest` does not move the number — it asserts manifests, not Go statements.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum .gitignore test/envtest Makefile .github/workflows/ci.yml
git commit -m "test(crd): all three schemas and CEL rules under envtest

NFR-01 waived a second time, for sigs.k8s.io/controller-runtime. The argument
is narrow: a CEL transition rule that errors rather than refuses rejects every
update, including the one that arms the object, and reads as a broken CRD
rather than a wrong rule. It never fires on the create, where there is no
oldSelf, so nothing catches it until something applies the right object in the
right order.

One mechanism per question. The two existing scripts stay: they are written,
passing, and assert the installed-and-served path through make deploy, which
this does not. Their 24 cases are ported here rather than reinvented, and
Step 4 asserts the port dropped nothing.

Runs in the unit job, not behind the kind cluster. Needing no cluster is most
of why it is worth having.

The suite exits 0 when the control-plane binaries are absent, so go test ./...
works on a bare machine — and CI therefore asserts it actually ran, because a
skip and a pass are the same summary line.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Acceptance criteria coverage

The two criteria no fake client can reach, because the structural schema and the CEL rules are evaluated by the API server and by nothing else.

| ID | Task | Why it cannot be a unit test |
| --- | --- | --- |
| AC-09 | 2 | 001's CRD installs and refuses what it should; the schema and its immutability rule are the API server's to enforce |
| AC-16 | 2 | 002's CRD installs and refuses an empty namespace list, a missing `resources.requests`, a `resources.limits` key, and any edit other than `approvedPlan` |

Both are also asserted through `kubectl` by the two `verify-crd-*.sh` scripts on a live cluster. Envtest adds them to the fast job, where they run on every pull request rather than only behind a kind cluster.

## Review Focus coverage

| # | Condition | Task, Step |
| --- | --- | --- |
| 1 | A CI cluster name that disagrees with the Makefile | 1 Step 2 |
| 2 | A transition rule that rejects everything | 2 Step 6 |
| 3 | An envtest suite that skipped rather than passed | 2 Steps 6 and 7 |

## Deviations and clarifications to confirm before merging

1. **RESOLVED — envtest is built, and NFR-01 is waived a second time.** An earlier draft of this plan declined it: the `verify-crd-*.sh` scripts already assert specific error substrings against a real API server, on a cluster Task 1 now starts anyway, so envtest looked like a second mechanism for a question already answered. The decision went the other way, and the spec now records the waiver and the reason. The argument that carries it is narrower than "more testing is better": a CEL transition rule that **errors** rather than refuses rejects every update including the one that arms the object, reads as a broken CRD rather than a wrong rule, and is invisible until something applies the right object in the right order. A Go test in the fast job is where that belongs.

   One consequence, deliberate: the two existing scripts stay exactly as they are, because they assert something envtest does not — that the manifests install through `make deploy` on a real cluster. Their 24 cases are ported into Task 2, and Step 4 asserts the port dropped none.

2. **CI runs the aggregates, not the individual scripts.** Task 1's `kind` job runs `make verify` and `make verify-crd`, which the refactor plan's Task 4 defines. Anything added later joins those aggregates rather than the workflow, so a new suite reaches CI by editing the Makefile and nothing else. The metrics plan adds one step to this workflow directly, because a metrics assertion is not a `verify-*` script.

3. **The unit job is not gated on the kind job.** They run in parallel. A broken unit suite and a broken cluster suite are different failures and a reviewer wants both in one pass, not the second one hidden behind the first.
