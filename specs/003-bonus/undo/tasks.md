# Undo for Workload Hardening — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A second CRD that removes the fields `WorkloadHardening` wrote, and holds the workloads it selects out of future hardening for as long as it exists.

**Architecture:** The inversion is a **pure function** in `pkg/plan` reading the provenance annotation 002 writes — there is no previous value to restore, because BR-01 only ever filled absent fields, so the inverse of every change is "delete this field". A third reconciler shares the existing binary, queue and single worker. The hold that keeps a reverted workload out of future hardening is two annotations that 002's own exclusion already honours, so **nothing in 002 changes**. Tasks 1–3 are the pure half and are testable before any client is involved, which is 002's shape and most of why this is cheap.

**Tech Stack:** Go 1.27.1 · `k8s.io/api` v0.37.1 · `k8s.io/apimachinery` v0.37.1 · `k8s.io/client-go` v0.37.1 · `k8s.io/klog/v2` v2.140.0. Vendored; **no new module dependencies**.

**Spec:** `specs/003-bonus/spec.md` — BR-U01…BR-U11, FR-U01…FR-U06, NFR-U01…NFR-U04, AC-U01…AC-U22, Error cases.

**Depends on:** the refactor plan's Task 3 for `plan.Request`. If it has not run, the argument is `plan.Policy` and nothing else changes — FR-U02 says so explicitly. **Verified by:** the integration-tests plan's Tasks 2 and 3, which own `hack/verify-crd-undo.sh` and `hack/verify-undo.sh` and therefore AC-U03's dry-run half, AC-U04, AC-U05, AC-U08, AC-U11 and AC-U12.

---

## Global Constraints

- **NFR-U01 — no new module dependencies.** `go.mod` and `go.sum` must be byte-identical before and after this plan. Everything the undo needs is already inside the three `k8s.io` modules and already in `vendor/`.
- **Nothing in 002 changes behaviourally.** BR-U10. The hold is written with `hardening.acme.corp/skip`, which `excluded()` in `pkg/controller/targets.go` already reads. Every existing test in `pkg/controller` must pass unedited except where this plan renames a shared identifier or changes a shared test helper's signature.
- **API group/version/kind:** `hardening.acme.corp` / `v1alpha1` / `WorkloadHardeningUndo`, plural `workloadhardeningundos`, short name `whu`, namespaced, status subresource.
- **Annotations, exactly:** `hardening.acme.corp/filled` and `hardening.acme.corp/skip` (both existing, both unchanged) and `hardening.acme.corp/skip-by` (new). Finalizer: `hardening.acme.corp/undo-release`. No others.
- **Caps (FR-U01):** `namespaces` 1–16 unique DNS labels; `workloadSelector` 1–8 `matchLabels` entries with no `matchExpressions`; `approvedPlan` up to 128 twelve-character lowercase hex hashes.
- **Protected namespaces (BR-U06, inheriting BR-05):** reuse the existing `Protected` set and `protectedNamespaces()` in `cmd/main/main.go` — the same flag, not a second one.
- **NFR-U02, in full.** No deletion of a path not recorded by this tool. No deletion of a value a human changed. No deletion admission would make unschedulable. No write before a dry-run of that same patch in the same pass. No patch without its annotation rewrite and its hold in the same request. No removal of a `skip` this tool did not write.
- **NFR-U03 — no new verbs on any core resource.** The only RBAC delta is `workloadhardeningundos`, its `status`, `update` on it for the finalizer, and `workloadhardeningundos/finalizers: update`.
- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1, exactly as `go.mod` declares.
- **The repository vendors.** `vendor/` is committed and `go build`/`go test` use it. Never run `go mod tidy`.
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (`AGENTS.md`), enforced by `make cover`. Today's total is 93.5% and the lowest package is 91.7%. `cmd/` is wiring and is excluded.
- **`AGENTS.md` role constraint:** do not edit `specs/003-bonus/spec.md`, and do not create or modify anything else under `specs/` except this file. If the implementation needs behaviour the spec does not describe, stop and raise it rather than inventing a requirement. Every such point this plan already found is listed in "Deviations and clarifications to confirm before merging" at the end.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `refactor:`, `docs:`, `build:`, `ci:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Eight conditions the spec implies but no acceptance criterion names, ordered by how likely each is to reach an operator.

1. **`capabilities.drop` recorded as the literal `[ALL]`, compared against a live `[]corev1.Capability{"ALL"}`.** The obvious first implementation compares the typed value to the record and never matches. It does not error — every undo then reports every container as edited by a human, reverts nothing, and reports `Applied` having done nothing. AC-U02 names the rendering; nothing names the failure mode, which is silence. Expected: the two renderings agree by construction, because both directions read one table. → **Task 1, Step 1** and **Task 2, Step 2.**
2. **A removal that passes the dry-run cleanly and halts the rollout afterwards.** BR-U04's two gates run against **pods**, not against the workload being patched, so neither appears in a dry-run. This is 002's AC-05 trap repeated at namespace scope, and the spec says so. Expected: in `enforce: restricted` the four always-on fields are skipped and reported while requests are still removed; a Container `min` with no default blocks that request alone. → **Task 2, Step 2**, and end to end in the integration-tests plan's Task 3.
3. **A hold that silently lapses.** An `Applied` object is otherwise inert, so a `skip` someone strips by hand would go unnoticed and the next hardening pass would refill what was just reverted. Expected: `Applied` is terminal for the **revert** while the hold comparison runs on every pass, and an object whose holds are all intact still issues no writes. → **Task 9, Step 1.**
4. **A malformed provenance annotation.** Hand-edited, truncated by a `kubectl edit`, or carrying a value with an `=` in it. Guessing at it would delete the wrong fields. Expected: one finding naming the object, no deletions, the annotation left exactly as it was, every other target unaffected — and the parser splits on the **first** `=`, because a path never contains one and a value might. → **Task 2, Step 2** and **Task 8, Step 1.**
5. **A `skip-by` whose UID names an object that no longer exists** — a finalizer force-cleared, or an object removed while the controller was down. Without take-over, one orphaned annotation blocks every future bypass of that workload forever and the only remedy is editing it by hand. Expected: it is not a claim. → **Task 6, Step 1** and **Task 7, Step 1.**
6. **A recorded path naming a container that no longer exists, or the wrong list.** `containers[app]` where the container was renamed, or an init container's record read against the regular list — the two may share a name, which is why `Build` keys their paths apart. Expected: "absent", never a panic and never a different container's value. → **Task 1, Step 1.**
7. **A hold patch that reaches `spec`.** The whole argument for not gating the hold behind `approvedPlan` is that two keys under `metadata.annotations` change no pod-template hash and start no rollout. A body that touches `spec.template` restarts every pod of every selected workload to write an annotation — the exact harm the feature exists to avoid, performed while previewing. → **Task 3, Step 1** and **Task 9, Step 1.**
8. **An undo of a workload a second `WorkloadHardening` patched after the first.** G-U01: the second annotation write replaced the first, so records only the first object wrote are simply gone. Expected: revert what the annotation says and claim nothing about the rest. → **Task 2, Step 2.**

**Checked and deliberately excluded.** Two undos previewing the same workload is **named** by the spec (BR-U11, "Two undos can both preview the same workload") rather than unnamed, so AC-U18 covers it and it spends no slot here. A `WorkloadHardeningUndo` racing a `WorkloadHardening` over one workload is excluded because the single worker serialises them — asserted at **Task 10, Step 1** as a property of the wiring rather than as a failure mode.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `pkg/plan/leaf.go` | **New.** Pure: resolves one recorded leaf path against a live `PodSpec` and renders it exactly as `Lines` does. The other half of `Build`, and the half that can be wrong quietly. |
| `pkg/plan/invert.go` | **New.** Pure: the provenance annotation, one template and one namespace's admission facts → the deletions, the findings, and the annotation that replaces it. |
| `pkg/plan/patch.go` | **Modified.** `Patch` takes its annotations instead of deriving them; `MetadataPatch` alongside it, for the hold. |
| `pkg/plan/resources.go` | **Modified.** `Coverage` gains `Floor` — the same LimitRanges BR-06 reads, asked the opposite question. |
| `pkg/plan/plan.go` | **Modified.** `Request` gains `Enforce`, the namespace's Pod Security label. |
| `pkg/apis/v1alpha1/undo.go` | **New.** The `WorkloadHardeningUndo` structs, GVR/GVK, `skip-by`, the finalizer, unstructured conversion. |
| `pkg/apis/v1alpha1/hardening.go` | **Modified.** Two `omitempty` holder fields on the shared `TargetStatus`, and the `Reverted` and `Held` outcomes. |
| `pkg/controller/targets.go` | **Modified.** `discover` becomes the free function `discoverWorkloads` with a selector and a skip toggle; `hardeningTarget` becomes `workloadTarget`, carrying the workload's own annotations. |
| `pkg/controller/hold.go` | **New.** Pure: what to write to claim a target, what to take back on delete, who holds a workload, which target is already someone else's. |
| `pkg/controller/undo.go` | **New.** `UndoReconciler`: validation, exclusivity, the inverse plan, the three patch shapes, the re-assert, the release. |
| `pkg/controller/controller.go` | **Modified.** A third watched resource on the same queue and the same single worker. |
| `cmd/main/main.go` | **Modified.** Constructs the third reconciler. |
| `deploy/crd-undo.yaml` | **New.** Structural schema, and the CEL transition rule with the `has()` guards an optional field needs. |
| `deploy/crd-hardening.yaml` | **Modified.** One description: it stops telling operators there is no undo, in the commit that makes that false. |
| `deploy/rbac.yaml` | **Modified.** The new resource, its status and its finalizers subresource. No new verbs on any core resource. |
| `deploy/samples/undo.yaml` | **New.** An unarmed undo over the hardening samples. |

---

### Task 1: The live-value reader (FR-U02, BR-U02, AC-U02)

**Files:**
- Create: `pkg/plan/leaf.go`
- Create: `pkg/plan/leaf_test.go`

**Interfaces:**
- Consumes: `templatePath`, `containersField`, `initContainersField`, `cutList` from `pkg/plan`.
- Produces: `plan.ReadLeaf(pod *corev1.PodSpec, path string) (value string, present bool)` — resolves a recorded leaf path against a live PodSpec and renders it exactly as `Lines` would. Task 2 is its only caller.

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

`requests(...)` is the existing helper in `pkg/plan/resources_test.go`. `Request` is `Policy` if the refactor plan has not run.

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

### Task 2: The inverse plan (FR-U02, BR-U02, BR-U03, BR-U04, AC-U02, AC-U03)

**Files:**
- Create: `pkg/plan/invert.go`
- Create: `pkg/plan/invert_test.go`
- Modify: `pkg/plan/plan.go` (add `Enforce` to `Request`)
- Modify: `pkg/plan/resources.go` (add `Floor` to `Coverage`)
- Modify: `pkg/plan/resources_test.go`

**Interfaces:**
- Consumes: `ReadLeaf` from Task 1.
- Produces:
  - `plan.Invert(pod *corev1.PodSpec, provenance string, req Request) (Plan, string)` — deletions in `Changes`, everything skipped in `Findings`, and the rewritten annotation value as the second result (`""` means remove the annotation). Task 7 is its only caller.
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

### Task 3: `Patch` takes its annotations (BR-U05, BR-U09, NFR-U02)

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

### Task 4: The API type and the CRD (FR-U01, FR-U06)

**Files:**
- Create: `pkg/apis/v1alpha1/undo.go`
- Create: `pkg/apis/v1alpha1/undo_test.go`
- Create: `deploy/crd-undo.yaml`
- Modify: `pkg/apis/v1alpha1/hardening.go` (two `TargetStatus` fields, one new outcome)
- Modify: `deploy/crd-hardening.yaml` (the description)
- Modify: `Makefile`

**Interfaces:**
- Consumes: nothing.
- Produces, all used from Task 5 onwards:
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

- [ ] **Step 6: Correct the hardening CRD description**

In `deploy/crd-hardening.yaml`, in the `openAPIV3Schema` description, replace `There is no undo.` with:

```
            Reverting is a separate kind, WorkloadHardeningUndo, which reads
            the provenance annotation this one writes and needs neither this
            object nor its status.
```

This is the commit where the old sentence becomes false, which is why the documentation plan leaves it alone — see Deviations item 5.

- [ ] **Step 7: Wire the CRD into deploy**

In the `Makefile`, add to `deploy`:

```make
	kubectl apply -f deploy/crd-undo.yaml
```

placed with the other two `kubectl apply -f deploy/crd*.yaml` lines. The
`verify-crd-undo` target and the script it runs belong to the integration-tests plan (its Task 2), which owns every `hack/verify-*.sh`; this task ships the schema, not its assertions.

- [ ] **Step 8: Verify the CRD installs and serves**

```bash
make kind-up || true
kubectl apply -f deploy/crd-undo.yaml
kubectl explain workloadhardeningundo.spec.workloadSelector
kubectl get crd workloadhardeningundos.hardening.acme.corp -o jsonpath='{.status.acceptedNames.kind}'
```

Expected: the CRD applies, `kubectl explain` prints the `workloadSelector` description — which is the reference documentation for that field, so read it as a reader would — and the accepted kind is `WorkloadHardeningUndo`. AC-U12's fifteen rejection cases are the integration-tests plan's Task 2; this step proves the schema is installable, not that it refuses what it should.

- [ ] **Step 9: Run the unit suite**

Run: `go test ./... -race && make cover`
Expected: every package `ok`, coverage at or above 90%.

- [ ] **Step 10: Commit**

```bash
git add pkg/apis/v1alpha1/undo.go pkg/apis/v1alpha1/undo_test.go pkg/apis/v1alpha1/hardening.go \
        deploy/crd-undo.yaml deploy/crd-hardening.yaml Makefile
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
the integration-tests plan's verify-crd-undo.sh exist for that.

Two additions to the shared TargetStatus for the holder, and one outcome the
spec does not name: Held, for a selected workload with no records. AC-U21
requires it to be visibly held, which needs a row, which needs an outcome.

crd-hardening.yaml stopped saying 'There is no undo.' in the commit that made
it false.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Share discovery between the two features (BR-U06, FR-U01, AC-U19)

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
  Task 7 calls `discoverWorkloads` with `HonourSkip: false`.

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

### Task 6: The hold, the release and exclusivity, as pure functions (BR-U09, BR-U10, BR-U11, AC-U20)

**Files:**
- Create: `pkg/controller/hold.go`
- Create: `pkg/controller/hold_test.go`

**Interfaces:**
- Consumes: `v1alpha1.SkipAnnotation`, `v1alpha1.SkipByAnnotation` from Task 4.
- Produces, all pure and all called from Task 7:
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

### Task 7: Fixtures, validation and exclusivity (FR-U05, BR-U04, BR-U11, AC-U07, AC-U18, AC-U19)

**Files:**
- Create: `pkg/controller/undo.go`
- Create: `pkg/controller/undo_test.go`

**Interfaces:**
- Consumes: the API types (Task 4), `discoverWorkloads` (Task 5), `firstConflict` (Task 6).
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

3. **`Reconcile` has a deletion branch** on `u.DeletionTimestamp != nil`, which Task 9 fills in. For now, return `nil` from it with a `TODO` that Task 9 replaces — and note it here so it is not mistaken for finished work.

- [ ] **Step 4: Run the tests until green**

Run: `go test ./pkg/controller/ -run 'TestHardenedFixture|TestUndo|TestTwoUndos' -v`
Expected: all five PASS.

- [ ] **Step 5: Run the whole suite**

Run: `go test ./... -race`
Expected: every package `ok`. Coverage may dip below 90% here because `undo.go` has paths no test reaches yet; Tasks 8 and 9 close it, and `make cover` is checked at the end of Task 9.

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

### Task 8: Preview and apply the revert (FR-U03, FR-U04, BR-U07, AC-U01, AC-U06, AC-U09, AC-U10)

**Files:**
- Modify: `pkg/controller/undo.go`
- Modify: `pkg/controller/undo_test.go`

**Interfaces:**
- Consumes: `plan.Invert` (Task 2), `plan.Patch` (Task 3), the dry-run cache in `execute.go`.
- Produces: `(*UndoReconciler).execute(ctx, logger, u, t workloadTarget, p plan.Plan, surviving string, row v1alpha1.TargetStatus) v1alpha1.TargetStatus` and `undoPhaseFor(u, rows) (v1alpha1.Phase, string)`. Task 9 adds the hold to the same patch.

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
- The patch body is `plan.Patch(p.Changes, annotations)` where `annotations` is `map[string]any{v1alpha1.FilledAnnotation: survivingOrNil(surviving)}`. Task 9 merges the hold into the same map.
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

### Task 9: The hold, the re-assert and the release (BR-U09, BR-U10, FR-U06, AC-U13 to AC-U17, AC-U21, AC-U22)

**Files:**
- Modify: `pkg/controller/undo.go`
- Modify: `pkg/controller/undo_test.go`

**Interfaces:**
- Consumes: `holdFor`, `releaseFor`, `holder` (Task 6), `plan.MetadataPatch` (Task 3).
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
3. Replace Task 7's deletion-branch TODO:
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

Expected: only the `execute.go` call-site change from Task 3, the `discoverWorkloads` call from Task 5, and the test-only `patchActions`/`storedTemplate` signature change. Any changed assertion in `hardening_test.go` means the hold moved 002's behaviour, which BR-U10 forbids.

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


---

### Task 10: Wiring, RBAC and manifests (FR-U05, NFR-U01, NFR-U03)

**Files:**
- Modify: `pkg/controller/controller.go`
- Modify: `pkg/controller/controller_test.go`
- Modify: `cmd/main/main.go`
- Modify: `deploy/rbac.yaml`
- Create: `deploy/samples/undo.yaml`

**Interfaces:**
- Consumes: `UndoReconciler` from Tasks 7–9, `v1alpha1.UndoResource` from Task 4.
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

## Acceptance criteria coverage

The unit half. AC-U03's dry-run clause, AC-U04, AC-U05, AC-U08, AC-U11 and AC-U12 are script-only per NFR-U04 and belong to the integration-tests plan — a fake client runs neither admission plugin nor the Deployment controller, and validates no API type at all.

| ID | Task | Evidence |
| --- | --- | --- |
| AC-U01 | 8 | `TestUndoRevertsEverythingItWrote` |
| AC-U02 | 1, 2 | `TestReadLeafRendersExactlyWhatBuildWrote`, `TestInvertLeavesHumanEditsAlone` |
| AC-U03 | 2 | `TestInvertSeccompDeletesTheParent` — the path only; the dry-run is the integration-tests plan's Task 3 |
| AC-U05 | 2 | `TestInvertLimitRangeGate` — the decision only; the live gate is the integration-tests plan's Task 3 |
| AC-U06 | 8 | `TestUndoPartialRewritesTheAnnotation` |
| AC-U07 | 5, 7 | `TestDiscoverHonourSkipIsOptional`, `TestUndoRejectsAProtectedNamespace`, `TestUndoRefusesTargetsThatWillNotRoll` |
| AC-U09 | 8 | `TestUndoPreviewWritesNoDeletion` |
| AC-U10 | 8 | `TestUndoRecreatedWorkloadHasNothingToRevert` |
| AC-U13 | 9 | Added at Task 9, Step 1 on `TestHardeningAndUndoConverge`'s fixture — see Deviations item 4 |
| AC-U14 | 9 | `TestHoldExcludesTheTargetFromHardening` |
| AC-U15 | 9 | `TestDeleteReleasesOnlyWhatItWrote` |
| AC-U16 | 9 | `TestHardeningAndUndoConverge` |
| AC-U17 | 8 | `TestUndoPartialRewritesTheAnnotation`'s closing assertion |
| AC-U18 | 6, 7 | `TestFirstConflict`, `TestUndoExclusivity` |
| AC-U19 | 5, 7 | `TestDiscoverSelectorMatchesTheWorkloadsOwnLabels`, `TestTwoUndosWithDisjointSelectorsBothProceed` |
| AC-U20 | 6, 9 | `TestReleaseFor`, `TestDeleteReleasesOnlyWhatItWrote` |
| AC-U21 | 9 | `TestUnarmedUndoHoldsWithoutReverting` |
| AC-U22 | 9 | `TestHoldIsReassertedAndOtherwiseSilent` |

## Review Focus coverage

| # | Condition | Task, Step |
| --- | --- | --- |
| 1 | `[ALL]` rendering | 1 Step 1, 2 Step 2 |
| 2 | A removal that halts the rollout | 2 Step 2 |
| 3 | A hold that silently lapses | 9 Step 1 |
| 4 | A malformed provenance annotation | 2 Step 2, 8 Step 1 |
| 5 | An orphaned `skip-by` | 6 Step 1, 7 Step 1 |
| 6 | A path naming a missing container | 1 Step 1 |
| 7 | A hold patch that reaches `spec` | 3 Step 1, 9 Step 1 |
| 8 | Two hardening objects in sequence | 2 Step 2 |

## Deviations and clarifications to confirm before merging

Per `AGENTS.md`, the Software Engineer may not invent requirements. Items 1–3 need a spec amendment or the user's assent before this plan merges; 4–6 are gaps closed on this plan's own authority and flagged; 7–9 are facts about the tooling.

1. **`plan.Invert` returns `(Plan, string)`, not `Plan`.** FR-U02 gives the signature as returning `Plan`. BR-U05 needs the records that **survived**, to rewrite the annotation in the same request — and `Plan` carries only `Changes` and `Findings`, while `plan.Finding` has no path field, so the survivors cannot be recovered from it. The second result is the rewritten annotation value, `""` meaning remove the key. The alternative is for the controller to reverse-map deletion paths back to records through BR-U03's table, which is `Invert`'s job and belongs beside it. Task 2.

2. **A per-target outcome the spec does not name: `Held`.** FR-U06 replaces `Patched` with `Reverted` and says nothing about a workload that is selected but carries no record. AC-U21 requires exactly such a workload to be visibly held, which needs a status row, which needs an outcome — and `Outcome` carries no `omitempty`, so leaving it blank would serialise as an empty string and read as a fault, which is 002's Deviation 2 repeated. The CRD declares `outcome` as a free string, so this costs vocabulary and no schema change. Task 4.

3. **FR-U05's "FR-05 unchanged" cannot be taken literally, and this plan does not.** FR-05 returns from an `Applied` object before it reads anything; BR-U09 requires the hold re-asserted on every resync; AC-U22 tests it. `Applied` becomes terminal for the **revert** — the deletion plan is not recomputed and no `spec.template` is touched again until `approvedPlan` moves — while the hold comparison runs on every pass. This is the spec's own position in BR-U09's prose and its FR-U05 text now matches; it is recorded because it is the one structural difference from 002's reconciler and a reader who skims will assume it is a copy. Task 9, commented at the guard.

4. **AC-U13 had no test in any task, and one is added.** The criterion fell between the revert task and the hold task. It is added at Task 9, Step 1, on `TestHardeningAndUndoConverge`'s fixture. The outcome is **stronger** than the criterion asks: once the hold is written, the reverted target is excluded by BR-04 and becomes a *finding*, not an `Unapproved` row, so it is never republished at all. The test asserts the stronger property and says why.

5. **`deploy/crd-hardening.yaml`'s "There is no undo." is corrected here, not in the refactor plan's docs pass.** That sentence is still **true** until this CRD exists, so correcting it earlier would make the repository briefly wrong in the other direction. Task 4, the commit that makes it false.

6. **`workloadTarget` gains an `Annotations` field.** Not named anywhere in the spec. The hold (BR-U09), the release (BR-U10) and the exclusivity check (BR-U11) all read three annotations off each selected workload, and discovery has the object in hand. Carrying the map out of discovery makes all three cost zero extra reads; the alternative is a second `Get` per target per pass. Task 5.

7. **`fake.Clientset` does not honour `DryRun`, and validates no API type.** Verified against `k8s.io/client-go@v0.37.1`; 002 found the first half and installs the `dryRunGuard` reactor, which this plan reuses unchanged. The second half is why AC-U03's dry-run clause is not a unit test: a fake client would accept a `seccompProfile: {}` patch that a real API server refuses on every target, and the criterion the spec calls one of the two most easily got wrong would pass green all the way to a cluster.

8. **Neither fake bumps `metadata.generation`.** 002's Deviation 12, inherited. The revert-terminality gate compares `generation` against `status.observedGeneration`, so the `arm` helper at Task 8 increments it explicitly, with a comment saying why.

9. **client-go's warning handler is per-`rest.Config`, not per-request.** FR-U03 says dry-run warnings are "captured through the client's warning handler". There is no per-call API on a typed clientset and one clientset serves every reconciler, so the capture is a sink the handler appends to and the caller drains around each patch. Correct only because `controller.go` runs a single worker — recorded because it is what breaks first if a second worker ever looks attractive, and it breaks by attributing one workload's warning to another rather than by failing.
