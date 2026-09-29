# On-demand Workload Hardening — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the existing controller binary so that a `WorkloadHardening` custom resource fills in missing resource requests and missing securityContext hardening across one to sixteen namespaces, after previewing every change and having each target approved by its own hash — without demoting or breaking the workloads being patched.

**Architecture:** The same shape as 001, in the same binary. `WorkloadHardening` is read and written through the **dynamic client** with `unstructured`, converted to hand-written Go structs in `pkg/apis/v1alpha1` — no code generation. Workloads, LimitRanges, Jobs, ReplicaSets and Pods use the typed clientset. The decision of what to write is a **pure function** in a new package `pkg/plan`, the counterpart of `pkg/policy` in 001: it takes one template and one policy and returns the leaf fields it would write plus the findings it observed, with no client and no cluster state. The same function feeds the dry-run, the real patch and the rendered status, so a preview cannot diverge from what is applied. A second informer feeds the existing workqueue and worker. There is **no finalizer, no workload informer and no drift repair** (FR-05, D-02).

**Tech Stack:** Go 1.27.1 · `k8s.io/client-go` v0.37.1 · `k8s.io/api` v0.37.1 · `k8s.io/apimachinery` v0.37.1 · `k8s.io/klog/v2` v2.140.0 · kind v0.32.0 · kubectl v1.36.2. Vendored (`vendor/`); no new module dependencies — `k8s.io/api/apps/v1`, `k8s.io/api/batch/v1`, `k8s.io/apimachinery/pkg/api/resource` and `k8s.io/client-go/kubernetes/typed/apps/v1` are all already inside the required modules and already present in `vendor/`.

**Spec:** `specs/002-workload-hardening/spec.md` (read it alongside this plan; every task cites the requirement it implements). 001's spec and plan are `specs/001-network-isolation/spec.md` and `specs/001-network-isolation/tasks.md`.

---

## Global Constraints

- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1, exactly as `go.mod` declares.
- **NFR-01 — no new module dependencies.** Everything this feature needs already lives inside `k8s.io/api@v0.37.1`, `k8s.io/apimachinery@v0.37.1` and `k8s.io/client-go@v0.37.1`. **After adding any new `k8s.io/*` import, run `go mod vendor` and commit the vendor changes in the same commit.** `go.mod` and `go.sum` must not change; if they do, something outside those three modules was imported and must be removed.
- **API group/version/kind:** `hardening.acme.corp` / `v1alpha1` / `WorkloadHardening`, plural `workloadhardenings`, short name `wh`, namespaced, status subresource.
  - **Group-string check, done.** The 002 spec says `hardening.acme.corp` (FR-01). 001's _shipped_ code agrees: `pkg/apis/v1alpha1/types.go:14`, `deploy/crd.yaml:4,6`, `deploy/rbac.yaml:26,31,34` and `hack/verify-crd.sh` all use `hardening.acme.corp`. The inconsistency is **inside 001's plan document only** — `specs/001-network-isolation/tasks.md` says `hardening.k8s.io` in its Global Constraints and in several code blocks, and `hardening.acme.corp` in the CRD it actually shipped. Nothing needs fixing in code; do **not** "correct" the shipped group to match 001's prose. `v1alpha1.GroupName` is the single source and is already right.
- **Annotations, exactly:** `hardening.acme.corp/filled` (provenance, BR-08) and `hardening.acme.corp/skip` (exclusion, BR-04). No others.
- **Protected namespaces (BR-05):** `kube-system`, `kube-public`, `kube-node-lease`, the controller's own namespace, plus anything on the existing `-protected-namespaces` flag. Reuse 001's `Reconciler.Protected` set and `protectedNamespaces()` in `cmd/main/main.go` — the same flag, not a second one.
- **Never write a resource limit.** No patch body, at any point, may contain a `limits` key (BR-03, D-07, AC-04). There is no `spec.resources.limits` field in the CRD.
- **Never overwrite a value that is already effectively present** (BR-01). The only write is into a gap; the inverse of every change is the deletion of a field.
- **Never write `runAsUser`, `fsGroup` or `privileged`** (BR-02, D-05).
- **`initContainers` are patched; `ephemeralContainers` are never patched and never read** (BR-02).
- **No finalizer, no workload informer, no drift repair** (FR-05, D-01, D-02). `Applied` is the only terminal phase; `Rejected` is re-evaluated on every resync.
- **Validation is all-or-nothing across namespaces; execution is per-target** (FR-05, BR-07).
- **NFR-04 — visibility.** Log the object UID, the decision taken, the per-target outcome with its change hash, and every refusal with its reason. An object with a blank phase is indistinguishable from one the controller has never seen, so a transient failure is reported too.
- **Change hashes are per target, never per plan** (BR-07). First 12 hex digits of SHA-256 over the canonical serialisation defined in Task 5.
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (NFR-05, AGENTS.md). `cmd/` is wiring and is excluded from that number.
- **AGENTS.md role constraint:** do not edit `specs/002-workload-hardening/spec.md`, and do not create or modify anything else under `specs/`. If the implementation needs behaviour the spec does not describe, stop and raise it rather than inventing a requirement. Every such point this plan already found is listed in "Deviations and clarifications to confirm before merging" at the end.
- **Do not disturb 001.** `pkg/apis/v1alpha1/types.go`, `pkg/policy/`, `pkg/controller/validate.go` and `pkg/controller/reconcile.go` are finished and merged. The only 001 file this plan modifies is `pkg/controller/controller.go` (Task 9), because one queue must now serve two resources.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `docs:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Six conditions the spec implies but which no acceptance criterion names, ordered by how likely each is to bite someone using this tool. Each has a test assigned to the task that owns the code.

1. **A change hash that moves between two passes over an unchanged target.** BR-07's whole gate rests on a canonical serialisation, and everything feeding it is a map: `spec.resources.requests`, `securityContext`, the container lists. Go randomises map iteration. An unsorted serialisation produces a different hash on most passes, so every approval the operator copies is `Stale` before the write lands, and nothing in the status says why. Expected: hashing the same target twice, and hashing two `Policy` values built from separately constructed maps, yields byte-identical output. → **Task 5, Step 6.**
2. **A namespace holding more than one LimitRange, or one LimitRange holding several `Container`-scoped items.** BR-06 says "a `Container`-scoped LimitRange" in the singular, but a namespace may hold any number and the API server applies all of them. Expected: coverage is their **union** (any of them supplying a request removes the gap) and the bounds are their **intersection** (any of them excluding a value rejects the namespace). Reading only the first one either reports a gap that does not exist or writes a value the API server will refuse at pod admission, which no dry-run catches. → **Task 4, Step 8.**
3. **An `approvedPlan` holding hashes that match nothing** — a copy-paste from a stale preview, or an approval landing after the targets were patched by someone else. Every target is then `Unapproved`, nothing fails, and nothing is `Stale`. The phases are defined over approved _targets_ while `approvedPlan` is a list of _hashes_, so an unmatched hash is invisible to both — and `Applied` is **terminal**. FR-06 now names this case explicitly; before it did not, and the literal reading stranded the approval forever. Expected: `PartiallyApplied` with a message naming the count, so the resync keeps looking. The same applies when only _some_ of the approved hashes dangle — one target patching successfully must not carry the object to a terminal phase while another approval matched nothing — so the fold counts dangling approvals rather than `Stale` rows, which also keeps the phase independent of the status history that `Stale` needs. → **Task 8, Steps 9 and 10.**
4. **A target that is already fully hardened.** FR-02 says "a target with no gaps yields no patch and no API call", but no acceptance criterion checks it: AC-08 covers the dry-run flag, AC-14 covers an `Applied` object. Expected: zero gaps means no patch body is built, no dry-run is issued and no plan row is published — which is also what makes AC-13's convergence work, because a patched target has no gaps on the retry. → **Task 7, Step 9.**
5. **A namespace that vanishes between validation and apply.** Validation reads namespaces first and is all-or-nothing; the workload `List` that follows returns an **empty list**, not `NotFound`, once the namespace is gone. Expected: the namespace contributes no targets, its approved hashes therefore match nothing, and the object does **not** report `Applied` claiming it hardened a namespace that no longer exists. → **Task 8, Step 11.**
6. **A stored object whose `spec` will not convert into the typed struct** — a quantity the CRD's pattern admits but `resource.Quantity` refuses, or an object stored before the schema tightened. `HardeningFromUnstructured` returns an error, `Reconcile` returns it, the queue retries forever and `status` stays blank, so the object is indistinguishable from one the controller has never seen. Expected: `Rejected` naming the field, written through the unstructured object the pass still holds. → **Task 7, Step 11.**

**Checked and deliberately excluded:** a container name colliding between `containers` and `initContainers`. BR-02 states that names are unique across the two lists within a pod, and the API server enforces it, so the condition cannot arise. The change-path format in Task 5 keeps the two lists distinct (`containers[app]` versus `initContainers[app]`) regardless, so no test is spent on it. Likewise, two `WorkloadHardening` objects naming the same namespace is **named** by the spec (G-03) as permitted with the second's annotation replacing the first's, so it is not an unnamed condition.

---

## File Structure

| File                                      | Responsibility                                                                                                                                                                                                                       |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `pkg/apis/v1alpha1/hardening.go`          | **New.** The `WorkloadHardening` structs, GVR/GVK, the three new phases, outcome and annotation constants, unstructured conversion. Same package as 001's `types.go`, which is not touched. No logic beyond two one-line predicates. |
| `pkg/plan/plan.go`                        | **New.** Pure: one template plus one policy → the leaf changes and the findings. securityContext, precedence, root evidence.                                                                                                         |
| `pkg/plan/resources.go`                   | **New.** Pure: effective requests (limits→requests), LimitRange coverage and bounds.                                                                                                                                                 |
| `pkg/plan/patch.go`                       | **New.** Pure: the canonical serialisation, the change hash, the provenance line rendering, and the strategic merge patch body.                                                                                                      |
| `pkg/controller/targets.go`               | **New.** Target discovery and exclusion per namespace, the per-kind patch closure, rollout mechanism and pod counts. Everything reported but not patched.                                                                            |
| `pkg/controller/hardening.go`             | **New.** `HardeningReconciler`: one pass — terminal check, validation, discovery, plan, status.                                                                                                                                      |
| `pkg/controller/execute.go`               | **New.** One target's fate: the dry-run cache, preview, per-target approval, apply, and the phase fold.                                                                                                                              |
| `pkg/controller/controller.go`            | **Modified.** One queue, two resources: keys carry the resource they came from, and `New` wires a second informer onto the existing dynamic factory.                                                                                 |
| `cmd/main/main.go`                        | **Modified.** Constructs the second reconciler and passes it to `New`. No new flags.                                                                                                                                                 |
| `deploy/crd-hardening.yaml`               | **New.** The `WorkloadHardening` CRD: structural schema, per-field CEL immutability leaving `approvedPlan` mutable, status subresource, printer columns.                                                                             |
| `deploy/rbac.yaml`                        | **Modified.** Adds the workload, LimitRange, Job and ReplicaSet rules and the new custom resource. No `delete` on any workload, no `resourcequotas`.                                                                                 |
| `deploy/samples/workloads-hardening.yaml` | **New.** `harden-a` and `harden-b`, a LimitRange, and five Deployments covering every finding class AC-15 names.                                                                                                                     |
| `deploy/samples/hardening.yaml`           | **New.** An example `WorkloadHardening`, unarmed.                                                                                                                                                                                    |
| `hack/verify-crd-hardening.sh`            | **New.** AC-16, on a live API server.                                                                                                                                                                                                |
| `hack/verify-hardening.sh`                | **New.** AC-15, on a live kind cluster.                                                                                                                                                                                              |
| `Makefile`                                | **Modified.** `deploy` installs the second CRD; `samples-hardening`, `verify-hardening`, `verify-crd-hardening`.                                                                                                                     |
| `README.md`                               | **Modified.** Core task 2's setup, decisions, limitations and time.                                                                                                                                                                  |

---

### Task 1: `WorkloadHardening` API types (FR-01)

A new file in the package 001 already owns. `GroupName`, `Version` and the `Phase` type are shared; `PhasePending` and `PhaseRejected` are reused verbatim. Nothing in `types.go` changes — if a step makes you want to edit it, stop.

The one thing here that is not boilerplate is `corev1.ResourceList`. It carries `resource.Quantity`, which implements `json.Marshaler` and `json.Unmarshaler`, and `runtime.DefaultUnstructuredConverter` honours both — so `{"cpu": "10m"}` becomes a real `Quantity` and comes back as a string. It also **canonicalises**: `1000m` round-trips as `1`. That matters beyond cosmetics, because the canonical form is what Task 5 hashes, so `1000m` and `1` are the same approval rather than two.

**Files:**

- Create: `pkg/apis/v1alpha1/hardening.go`
- Test: `pkg/apis/v1alpha1/hardening_test.go`
- Do not modify: `pkg/apis/v1alpha1/types.go`

**Interfaces:**

- Consumes: `v1alpha1.GroupName`, `v1alpha1.Version`, `v1alpha1.Phase`, `v1alpha1.PhasePending`, `v1alpha1.PhaseRejected` (001, `types.go`).
- Produces:
  - `v1alpha1.HardeningKind`, `v1alpha1.FilledAnnotation`, `v1alpha1.SkipAnnotation`
  - `v1alpha1.HardeningResource` (`schema.GroupVersionResource`), `v1alpha1.HardeningGroupVersionKind`
  - `v1alpha1.PhasePreviewed`, `v1alpha1.PhaseApplied`, `v1alpha1.PhasePartiallyApplied`
  - `v1alpha1.OutcomePlanned`, `OutcomePatched`, `OutcomeFailed`, `OutcomeStale`, `OutcomeUnapproved`
  - `v1alpha1.WorkloadHardening`, `HardeningSpec`, `ResourcePolicy`, `SecurityPolicy`, `HardeningStatus`, `TargetStatus`, `Finding`
  - `func (*WorkloadHardening) Armed() bool`, `func (*WorkloadHardening) Approved(hash string) bool`
  - `func HardeningFromUnstructured(*unstructured.Unstructured) (*WorkloadHardening, error)`
  - `func HardeningToUnstructured(*WorkloadHardening) (*unstructured.Unstructured, error)`

- [ ] **Step 1: Write the failing test**

Create `pkg/apis/v1alpha1/hardening_test.go`:

```go
package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A dynamic-client object must survive the trip into the typed struct and back
// with everything the reconciler depends on intact. The quantities are the part
// that is not obvious: resource.Quantity implements json.Marshaler and
// json.Unmarshaler, and DefaultUnstructuredConverter honours both, so the
// strings become real Quantity values and come back canonicalised.
func TestHardeningRoundTrip(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": GroupName + "/" + Version,
		"kind":       HardeningKind,
		"metadata": map[string]any{
			"name":      "tenant-hardening",
			"namespace": "isolation-system",
			"uid":       "1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708",
		},
		"spec": map[string]any{
			"namespaces": []any{"tenant-a", "tenant-b"},
			"resources": map[string]any{
				// 1000m is written deliberately: it must come back as "1".
				"requests": map[string]any{"cpu": "1000m", "memory": "32Mi"},
			},
			"securityContext": map[string]any{"readOnlyRootFilesystem": true},
			"approvedPlan":    []any{"0123456789ab"},
		},
	}}

	w, err := HardeningFromUnstructured(in)
	if err != nil {
		t.Fatalf("HardeningFromUnstructured: %v", err)
	}
	if string(w.UID) != "1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708" {
		t.Errorf("UID = %q", w.UID)
	}
	if len(w.Spec.Namespaces) != 2 || w.Spec.Namespaces[1] != "tenant-b" {
		t.Errorf("namespaces = %v", w.Spec.Namespaces)
	}
	if !w.Spec.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("readOnlyRootFilesystem lost in conversion")
	}

	// Quantity.String has a pointer receiver, so a map value must be copied to
	// a local before it can be rendered. Writing w.Spec.Resources.Requests[k].String()
	// does not compile.
	cpu := w.Spec.Resources.Requests[corev1.ResourceCPU]
	memory := w.Spec.Resources.Requests[corev1.ResourceMemory]
	if got := cpu.String(); got != "1" {
		t.Errorf("cpu = %q, want the canonical form %q of 1000m", got, "1")
	}
	if got := memory.String(); got != "32Mi" {
		t.Errorf("memory = %q, want 32Mi", got)
	}

	out, err := HardeningToUnstructured(w)
	if err != nil {
		t.Fatalf("HardeningToUnstructured: %v", err)
	}
	if out.GetKind() != HardeningKind || out.GetAPIVersion() != GroupName+"/"+Version {
		t.Errorf("GVK = %s %s", out.GetAPIVersion(), out.GetKind())
	}
	spec, _ := out.Object["spec"].(map[string]any)
	resources, _ := spec["resources"].(map[string]any)
	requests, _ := resources["requests"].(map[string]any)
	if requests["cpu"] != "1" {
		t.Errorf("spec.resources.requests.cpu = %v, want the canonical \"1\"", requests["cpu"])
	}
	if _, present := resources["limits"]; present {
		t.Error("a limits key reached the wire; limits are never written (BR-03)")
	}
}

// Zero is an observation, not an absence: a target affecting zero pods must
// still report the count (FR-03, and 001's rule for matched counts).
func TestZeroPodsIsSerialised(t *testing.T) {
	w := &WorkloadHardening{Status: HardeningStatus{
		Phase: PhasePreviewed,
		Plan: []TargetStatus{{
			Namespace: "tenant-a", Kind: "Deployment", Name: "api",
			Hash: "0123456789ab", Pods: 0, Outcome: OutcomePlanned,
		}},
	}}
	out, err := HardeningToUnstructured(w)
	if err != nil {
		t.Fatalf("HardeningToUnstructured: %v", err)
	}
	status, ok := out.Object["status"].(map[string]any)
	if !ok {
		t.Fatal("status missing")
	}
	rows, ok := status["plan"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("status.plan = %v", status["plan"])
	}
	row := rows[0].(map[string]any)
	if _, ok := row["pods"]; !ok {
		t.Error("pods dropped when zero")
	}
}

// Armed and Approved are the whole of BR-07's gate as the API types see it:
// empty or absent approvedPlan means preview only, and a hash is approved only
// if it is literally in the list.
func TestArmedAndApproved(t *testing.T) {
	unarmed := &WorkloadHardening{}
	if unarmed.Armed() {
		t.Error("an object with no approvedPlan must be unarmed")
	}
	empty := &WorkloadHardening{Spec: HardeningSpec{ApprovedPlan: []string{}}}
	if empty.Armed() {
		t.Error("an object with an empty approvedPlan must be unarmed")
	}

	armed := &WorkloadHardening{Spec: HardeningSpec{ApprovedPlan: []string{"0123456789ab", "cafebabe1234"}}}
	if !armed.Armed() {
		t.Error("an object with hashes must be armed")
	}
	if !armed.Approved("cafebabe1234") {
		t.Error("a listed hash must be approved")
	}
	if armed.Approved("0123456789AB") {
		t.Error("hashes are lowercase hex; the comparison must not fold case")
	}
	if armed.Approved("") {
		t.Error("the empty string must never count as approved")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/apis/... -run 'TestHardening|TestZeroPods|TestArmed' -v`
Expected: FAIL — build error, `undefined: HardeningKind`, `undefined: HardeningFromUnstructured`, `undefined: WorkloadHardening`.

- [ ] **Step 3: Write the types**

Create `pkg/apis/v1alpha1/hardening.go`:

```go
package v1alpha1

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The WorkloadHardening API. It shares GroupName, Version and the Phase type
// with NetworkIsolation in types.go, which this file does not touch.
const (
	HardeningKind = "WorkloadHardening"

	// FilledAnnotation is written onto every patched target, in the same
	// request as the patch, recording the leaf path of each field written and
	// the value written to it. It lives on the target rather than only in
	// status because status dies with the custom resource — one-shot semantics
	// and no finalizer mean deleting the object destroys the record (BR-08).
	FilledAnnotation = "hardening.acme.corp/filled"
	// SkipAnnotation, set to "true" on a workload, excludes it from targeting.
	// It is the escape hatch for a workload that genuinely needs what the
	// policy would take away (BR-04).
	SkipAnnotation = "hardening.acme.corp/skip"
)

// HardeningResource is the GVR the dynamic client uses for WorkloadHardening.
var HardeningResource = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "workloadhardenings"}

// HardeningGroupVersionKind stamps unstructured objects on the way out.
var HardeningGroupVersionKind = schema.GroupVersionKind{Group: GroupName, Version: Version, Kind: HardeningKind}

// The three phases this feature adds. PhasePending and PhaseRejected are
// shared with NetworkIsolation. Applied is the only terminal one; Rejected is
// re-evaluated on every resync, so an object refused for a missing namespace
// recovers by itself once the cause clears (FR-05, FR-06).
const (
	PhasePreviewed        Phase = "Previewed"
	PhaseApplied          Phase = "Applied"
	PhasePartiallyApplied Phase = "PartiallyApplied"
)

// Per-target outcomes (FR-06). Unapproved is not a failure: approving a subset
// is the expected use of a per-target gate. Stale is — the operator approved a
// change that no longer exists.
const (
	// OutcomePlanned is a target the plan would change, on an object nobody
	// has armed. FR-06 names no preview outcome; see the plan's deviations.
	OutcomePlanned    = "Planned"
	OutcomePatched    = "Patched"
	OutcomeFailed     = "Failed"
	OutcomeStale      = "Stale"
	OutcomeUnapproved = "Unapproved"
)

// ResourcePolicy is the value to write into an absent request. There is no
// Limits field and there never will be: the tool cannot know a workload's
// working set, and one number spread across sixteen namespaces is guaranteed
// wrong for some of them. LimitRange is the mechanism for limits (BR-03, D-07).
type ResourcePolicy struct {
	Requests corev1.ResourceList `json:"requests"`
}

// SecurityPolicy carries the one opt-in field. The other four are decided by
// BR-02, not by the operator. readOnlyRootFilesystem is opt-in because its
// failure is reliably late: a container that writes to its filesystem
// generally does so after it is serving, so nothing halts the rollout and
// every pod has already been replaced (BR-02, D-06).
type SecurityPolicy struct {
	ReadOnlyRootFilesystem bool `json:"readOnlyRootFilesystem"`
}

// HardeningSpec is immutable except for ApprovedPlan, enforced by a CEL
// transition rule in the CRD. Retargeting means a new object (BR-07, FR-01).
type HardeningSpec struct {
	Namespaces      []string       `json:"namespaces"`
	Resources       ResourcePolicy `json:"resources"`
	SecurityContext SecurityPolicy `json:"securityContext"`
	// ApprovedPlan is a list of change hashes. Empty or absent, the object is
	// unarmed: the plan is computed, dry-run and published, and nothing is
	// written (BR-07).
	ApprovedPlan []string `json:"approvedPlan,omitempty"`
}

// TargetStatus is one row of the rendered plan: the object reference, its
// change hash, the fields that would be set with their values, the number of
// pods affected, the rollout mechanism that applies, and, for anything not
// patched, the reason (FR-03).
//
// Pods carries no omitempty: zero is an observation and is reported
// explicitly, as in 001.
type TargetStatus struct {
	Namespace string   `json:"namespace"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Hash      string   `json:"hash"`
	Fields    []string `json:"fields,omitempty"`
	Pods      int      `json:"pods"`
	Rollout   string   `json:"rollout,omitempty"`
	Outcome   string   `json:"outcome"`
	Reason    string   `json:"reason,omitempty"`
}

// Finding is something observed and reported but not patched, with the reason.
// Findings are enumerated whether or not they can be acted on: a namespace
// reported as hardened while a root pod runs in it is a lie, and silence is
// how that lie gets told (BR-04).
type Finding struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Container string `json:"container,omitempty"`
	Reason    string `json:"reason"`
}

// HardeningStatus is written only when something other than the timestamp
// changed, so a resync of an unchanged object issues no writes at all (FR-06).
type HardeningStatus struct {
	Phase    Phase          `json:"phase,omitempty"`
	Message  string         `json:"message,omitempty"`
	Plan     []TargetStatus `json:"plan,omitempty"`
	Findings []Finding      `json:"findings,omitempty"`
	// ObservedGeneration is the metadata.generation of the spec this status
	// describes. Applied is terminal only while it matches, because
	// approvedPlan is the only mutable field: a generation ahead of this one
	// is an operator extending or correcting an approval, and without the
	// comparison the sole editable field would be ignored from the first
	// apply onwards (FR-05).
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastReconcileTime  string `json:"lastReconcileTime,omitempty"`
}

type WorkloadHardening struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              HardeningSpec   `json:"spec"`
	Status            HardeningStatus `json:"status,omitempty"`
}

// Armed reports whether the operator has approved anything. An unarmed object
// previews and writes nothing (BR-07).
func (w *WorkloadHardening) Armed() bool { return len(w.Spec.ApprovedPlan) > 0 }

// Approved reports whether hash appears in spec.approvedPlan. Hashes are
// lowercase hex and are compared literally: a hash is either the one the
// operator saw or it is not.
func (w *WorkloadHardening) Approved(hash string) bool {
	if hash == "" {
		return false
	}
	return slices.Contains(w.Spec.ApprovedPlan, hash)
}

// HardeningFromUnstructured converts an object read through the dynamic client.
func HardeningFromUnstructured(u *unstructured.Unstructured) (*WorkloadHardening, error) {
	w := &WorkloadHardening{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, w); err != nil {
		return nil, err
	}
	return w, nil
}

// HardeningToUnstructured converts back for a write through the dynamic client.
func HardeningToUnstructured(w *WorkloadHardening) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(w)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(HardeningGroupVersionKind)
	return u, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/apis/... -cover -v`
Expected: PASS, including 001's two existing tests. Coverage ≥90%.

If `TestHardeningRoundTrip` fails with `cpu = "1000m"`, the field is typed `map[string]string` rather than `corev1.ResourceList` — the canonical form is what Task 5 hashes, so fix the type, not the assertion.

- [ ] **Step 5: Commit**

```bash
go mod vendor
git add pkg/apis vendor go.mod go.sum
git commit -m "feat(api): add WorkloadHardening types and unstructured conversion

Shares GroupName, Version and Phase with NetworkIsolation; types.go is
unchanged. Requests are corev1.ResourceList so quantities arrive
canonicalised, which is what the per-target change hash is taken over."
```

`go.mod` and `go.sum` must be unchanged by this commit (NFR-01). If `git status` shows them modified, something outside `k8s.io/api`, `k8s.io/apimachinery` and `k8s.io/client-go` was imported.

---

### Task 2: CRD manifest and API-server validation script (FR-01, AC-16)

The schema is the first line of defence: it rejects shapes the controller then never has to handle. Two things here are not like 001.

**The immutability rule is per field, not on `spec`.** 001 uses `self == oldSelf` on the whole `spec`, which is exactly what must _not_ happen here: `approvedPlan` is the only mutable field, and the entire workflow is "copy hashes into `approvedPlan`". The rule therefore compares the three immutable fields individually. It works because `securityContext` carries a schema `default`, so it is always present after defaulting and the rule needs no `has()` guard.

**The quantity pattern must be the tight one.** The regular expression `resource.ParseQuantity` quotes in its own error message — `^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$` — is only the first of its two checks: it accepts `10mm` and `1e`, which `ParseQuantity` then refuses with "unable to parse quantity's suffix". A CRD carrying that looser pattern lets a value through that the Go conversion cannot read, which is Review Focus 6. Use the full grammar below.

**Files:**

- Create: `deploy/crd-hardening.yaml`, `hack/verify-crd-hardening.sh`
- Modify: `Makefile`

**Interfaces:**

- Consumes: the field names from `pkg/apis/v1alpha1/hardening.go` (Task 1). The schema and the Go structs must agree exactly.
- Produces: an installable CRD; `make verify-crd-hardening`.

- [ ] **Step 1: Write the CRD**

Create `deploy/crd-hardening.yaml`:

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: workloadhardenings.hardening.acme.corp
spec:
  group: hardening.acme.corp
  scope: Namespaced
  names:
    plural: workloadhardenings
    singular: workloadhardening
    kind: WorkloadHardening
    shortNames: [wh]
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      additionalPrinterColumns:
        - name: Phase
          type: string
          jsonPath: .status.phase
        - name: Namespaces
          type: string
          jsonPath: .spec.namespaces
        - name: Approved
          type: string
          jsonPath: .spec.approvedPlan
        - name: Message
          type: string
          jsonPath: .status.message
        - name: Age
          type: date
          jsonPath: .metadata.creationTimestamp
      schema:
        openAPIV3Schema:
          type: object
          description: >-
            Fills in missing resource requests and missing securityContext
            hardening across one or more namespaces. Creating the object
            requests a preview; copying hashes out of status.plan into
            spec.approvedPlan requests those patches. There is no undo.
          required: [spec]
          properties:
            spec:
              type: object
              required: [namespaces, resources]
              x-kubernetes-validations:
                # approvedPlan is the only mutable field. Everything else is
                # immutable, so retargeting means a new object (BR-07). The
                # three comparisons need no has() guards: namespaces and
                # resources are required, and securityContext carries a
                # default, so all three are always present after defaulting.
                - rule: >-
                    self.namespaces == oldSelf.namespaces &&
                    self.resources == oldSelf.resources &&
                    self.securityContext == oldSelf.securityContext
                  message: >-
                    only spec.approvedPlan may be changed; delete this object
                    and create a new one to retarget
              properties:
                namespaces:
                  type: array
                  description: >-
                    1-16 namespaces to harden, named explicitly. An explicit
                    list is what keeps the blast radius readable in the object
                    itself (D-11).
                  minItems: 1
                  maxItems: 16
                  x-kubernetes-list-type: set
                  items:
                    type: string
                    maxLength: 63
                    pattern: "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
                resources:
                  type: object
                  required: [requests]
                  properties:
                    requests:
                      type: object
                      description: >-
                        The value written into an absent request. Both cpu and
                        memory are required.
                      required: [cpu, memory]
                      properties:
                        cpu:
                          type: string
                          pattern: '^(\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))))?$'
                        memory:
                          type: string
                          pattern: '^(\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))))?$'
                    limits:
                      type: object
                      description: >-
                        Not supported, and never will be. Limits are never
                        written: the tool cannot know a workload's working set,
                        and a memory limit that is too small kills the
                        container after a rollout that completed green. Add a
                        LimitRange to the namespace instead (BR-03, D-07).
                      maxProperties: 0
                      additionalProperties:
                        type: string
                securityContext:
                  type: object
                  default: { readOnlyRootFilesystem: false }
                  properties:
                    readOnlyRootFilesystem:
                      type: boolean
                      default: false
                      description: >-
                        Opt-in. Not part of the restricted Pod Security
                        Standard, and the one policy field whose failure is
                        reliably late (BR-02, D-06).
                approvedPlan:
                  type: array
                  description: >-
                    Change hashes copied out of status.plan[].hash. The only
                    mutable field. A plan larger than 128 is approved in
                    batches, which per-target semantics make safe.
                  maxItems: 128
                  items:
                    type: string
                    pattern: "^[0-9a-f]{12}$"
            status:
              type: object
              properties:
                phase:
                  type: string
                  enum:
                    [Pending, Rejected, Previewed, Applied, PartiallyApplied]
                message:
                  type: string
                plan:
                  type: array
                  description: One row per target the plan would change.
                  items:
                    type: object
                    properties:
                      namespace: { type: string }
                      kind: { type: string }
                      name: { type: string }
                      hash: { type: string }
                      fields:
                        type: array
                        items: { type: string }
                      pods: { type: integer }
                      rollout: { type: string }
                      outcome: { type: string }
                      reason: { type: string }
                findings:
                  type: array
                  description: Observed and reported, never patched.
                  items:
                    type: object
                    properties:
                      namespace: { type: string }
                      kind: { type: string }
                      name: { type: string }
                      container: { type: string }
                      reason: { type: string }
                # The generation of the spec this status describes. Applied is
                # terminal only while it matches metadata.generation, because
                # approvedPlan is the only mutable field and an edit to it must
                # be acted on (FR-05).
                observedGeneration:
                  type: integer
                  format: int64
                lastReconcileTime:
                  type: string
                  format: date-time
```

- [ ] **Step 2: Bring a cluster up and install the CRD**

```bash
kind create cluster --name hardening --config hack/kind/cluster.yaml
kubectl apply -f deploy/crd-hardening.yaml
kubectl get crd workloadhardenings.hardening.acme.corp
```

Expected: `customresourcedefinition.apiextensions.k8s.io/workloadhardenings.hardening.acme.corp created`, then the CRD listed. A schema error appears here, not later. If the cluster already exists from 001, skip `kind create`.

- [ ] **Step 3: Write the validation script**

Create `hack/verify-crd-hardening.sh`:

```bash
#!/usr/bin/env bash
# AC-16: the CRD installs and the API server rejects an empty namespace list, a
# missing resources.requests, a resources.limits key, and an edit to any field
# other than approvedPlan.
set -euo pipefail

ns=hardening-crd-test
fail=0

cleanup() { kubectl delete namespace "$ns" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

# expect_reject <description> <expected-error-substring> <<<manifest
#
# The expected substring is not decoration: without it a manifest that fails to
# parse, or one rejected for an unrelated reason, reads as a passing test.
expect_reject() {
  local what=$1 want=$2 out
  if out=$(kubectl apply -f - 2>&1); then
    echo "FAIL  accepted: $what"
    fail=1
  elif ! printf '%s' "$out" | grep -qF "$want"; then
    echo "FAIL  rejected for the wrong reason: $what"
    echo "        want substring: $want"
    echo "        got: $(printf '%s' "$out" | head -2 | tr '\n' ' ')"
    fail=1
  else
    echo "ok    rejected: $what"
  fi
}

expect_accept() {
  local what=$1 out
  if out=$(kubectl apply -f - 2>&1); then
    echo "ok    accepted: $what"
  else
    echo "FAIL  rejected: $what"
    echo "        got: $(printf '%s' "$out" | head -2 | tr '\n' ' ')"
    fail=1
  fi
}

kubectl apply -f deploy/crd-hardening.yaml >/dev/null
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

expect_reject "empty namespace list" "should have at least 1 items" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: no-namespaces, namespace: $ns}
spec:
  namespaces: []
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

expect_reject "seventeen namespaces" "must have at most 16 items" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: too-many, namespace: $ns}
spec:
  namespaces: [n01, n02, n03, n04, n05, n06, n07, n08, n09, n10, n11, n12, n13, n14, n15, n16, n17]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

expect_reject "duplicate namespace" "Duplicate value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: duplicate-ns, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-a]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

expect_reject "resources omitted entirely" "spec.resources: Required value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: no-resources, namespace: $ns}
spec:
  namespaces: [tenant-a]
EOF

expect_reject "requests omitted" "spec.resources.requests: Required value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: no-requests, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {}
EOF

expect_reject "requests naming only cpu" "spec.resources.requests.memory: Required value" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: cpu-only, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {requests: {cpu: 10m}}
EOF

expect_reject "a resources.limits key" "limits: Too many" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: with-limits, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources:
    requests: {cpu: 10m, memory: 32Mi}
    limits: {cpu: 500m, memory: 1Gi}
EOF

# Review Focus 6, at the API-server level: the loose regular expression that
# ParseQuantity quotes in its error message accepts "10mm", and the Go
# conversion then cannot read it. The tight grammar in the schema must refuse
# it here, before it is ever stored.
expect_reject "a quantity with a two-character suffix" "spec.resources.requests.cpu in body should match" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: bad-quantity, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {requests: {cpu: 10mm, memory: 32Mi}}
EOF

expect_reject "a thirteen-character hash" "approvedPlan[0] in body should match" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: bad-hash, namespace: $ns}
spec:
  namespaces: [tenant-a]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  approvedPlan: [0123456789abc]
EOF

expect_accept "a valid unarmed object" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
EOF

# approvedPlan is the only mutable field: arming an existing object is the
# entire workflow and must be accepted (BR-07, FR-01).
expect_accept "arming the object by adding approvedPlan" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

expect_reject "an edit to spec.namespaces" "only spec.approvedPlan may be changed" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-c]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

expect_reject "an edit to spec.resources.requests" "only spec.approvedPlan may be changed" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 500m, memory: 32Mi}}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

expect_reject "an edit to spec.securityContext" "only spec.approvedPlan may be changed" <<EOF
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata: {name: valid, namespace: $ns}
spec:
  namespaces: [tenant-a, tenant-b]
  resources: {requests: {cpu: 10m, memory: 32Mi}}
  securityContext: {readOnlyRootFilesystem: true}
  approvedPlan: [0123456789ab, cafebabe1234]
EOF

if [ "$fail" -ne 0 ]; then
  echo "AC-16 FAILED"
  exit 1
fi
echo "AC-16 PASSED"
```

- [ ] **Step 4: Run it**

```bash
chmod +x hack/verify-crd-hardening.sh
./hack/verify-crd-hardening.sh
```

Expected: every line `ok`, final `AC-16 PASSED`.

Three things that go wrong here, and what they mean:

- **"a `resources.limits` key" is _accepted_.** The field is being pruned rather than rejected. Structural schemas prune unknown fields silently, so `limits` has to be _declared_ with `maxProperties: 0` — the same trick 001 uses for `matchExpressions` with `maxItems: 0`. Do not try `x-kubernetes-validations` with `has(self.limits)`: CEL cannot reference a field the schema does not declare.
- **"an edit to spec.securityContext" is _accepted_.** The `default: {readOnlyRootFilesystem: false}` on `securityContext` is missing, so `oldSelf.securityContext` is absent on an object created without it and the comparison short-circuits. Both defaults — on the object and on the boolean — are load-bearing.
- **"arming the object" is _rejected_.** The immutability rule is on the whole `spec` (001's `self == oldSelf`) instead of the three fields. That would make the entire feature unusable.

- [ ] **Step 5: Add Makefile targets**

Append to `Makefile`:

```make
verify-crd-hardening:
	./hack/verify-crd-hardening.sh
```

and add `verify-crd-hardening` to the `.PHONY` line.

- [ ] **Step 6: Commit**

```bash
git add deploy/crd-hardening.yaml hack/verify-crd-hardening.sh Makefile
git commit -m "feat(crd): WorkloadHardening schema with approvedPlan the only mutable field

Per-field CEL transition rules rather than 001's whole-spec rule, because
arming an existing object is the entire workflow. The quantity pattern is
the full grammar, not the looser expression ParseQuantity quotes in its
error message, which accepts suffixes it then refuses.

Adds hack/verify-crd-hardening.sh covering AC-16 against a live API server."
```

---

### Task 3: The plan — securityContext (BR-01, BR-02, AC-01, AC-02, AC-04, AC-05)

The pure core, and the half of it the spec flags as most easily got wrong. `pkg/plan` is the counterpart of `pkg/policy` in 001: no client, no context, no clock. It models Kubernetes' own defaulting, because none of it is visible in the template.

Two rules carry this task and both are counter-intuitive:

**A pod-level field is a gap only if writing it would reach something.** `runAsNonRoot` and `seccompProfile` are written at pod level. If every container already declares the field for itself, the container wins and a pod-level write changes nothing — so there is no gap (AC-02's "a container-level `true` with nothing at pod level is left alone"). If any container does not declare it, there is.

**One container evidencing a need for root suppresses pod-level `runAsNonRoot` for the whole pod.** Reporting that container as a finding — which reads as "left alone" — while still writing the pod-level field is the single most likely way this tool takes out a DaemonSet, because CNI agents, log shippers and node exporters are routinely privileged without ever declaring `runAsUser: 0`. No dry-run refuses it; the pod fails with `CreateContainerConfigError` at the kubelet.

A third decision this task makes, which the spec implies rather than states: **an explicitly privileged container receives no securityContext change at all.** BR-02 says dropping its capabilities would be theatre, BR-04 lists it under "reported as findings, never patched", and the API server rejects `allowPrivilegeEscalation: false` alongside `privileged: true` outright — so writing it would fail the whole target's dry-run and take its sibling containers down with it. The pod-level `seccompProfile` and the other containers' fields are still written, which is what AC-05's "the other three fields are still written" asks for.

The two exemptions do not rest on the same ground, and the code comment says which is which. `allowPrivilegeEscalation` is **refused by the API server** — verified on kind, `cannot set allowPrivilegeEscalation to false and privileged to true` — so it cannot be written at any price. `capabilities.drop: ["ALL"]` beside `privileged: true` is **accepted**, and is omitted purely on BR-02's judgment that it would be theatre. Getting the first wrong costs the whole target's patch at the dry-run, not a late kubelet failure.

**Files:**

- Create: `pkg/plan/plan.go`
- Test: `pkg/plan/plan_test.go`

**Interfaces:**

- Consumes: nothing from this project. `corev1` only.
- Produces:
  - `type plan.Target struct { Namespace, Kind, Name string }` with `func (Target) String() string`
  - `type plan.Change struct { Path, Value string; JSON any }`
  - `type plan.Finding struct { Container, Reason string }`
  - `type plan.Plan struct { Changes []Change; Findings []Finding }`
  - `type plan.Policy struct { Requests corev1.ResourceList; ReadOnlyRootFilesystem bool; Coverage Coverage }` — `Coverage` is declared in Task 4; declare it there, not here.
  - `func plan.Build(pod *corev1.PodSpec, policy Policy) Plan`

  Task 4 adds `(*Plan).buildResources` and `Coverage` to the same package; Task 5 adds `Canonical`, `Hash`, `Lines`, `Provenance` and `Patch`.

- [ ] **Step 1: Write the failing always-on-fields test (AC-01, AC-04)**

Create `pkg/plan/plan_test.go`:

```go
package plan

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// basic is a policy that fills both requests and nothing else, with no
// LimitRange in the namespace.
func basic() Policy {
	return Policy{Requests: requests("10m", "32Mi")}
}

// paths returns the change paths, which are already sorted by Build.
func paths(p Plan) []string {
	out := make([]string, 0, len(p.Changes))
	for _, c := range p.Changes {
		out = append(out, c.Path)
	}
	return out
}

// securityPaths returns only the securityContext change paths. Task 4 adds the
// resource ones and asserts the complete list; this task owns the
// securityContext half.
func securityPaths(p Plan) []string {
	var out []string
	for _, c := range p.Changes {
		if strings.Contains(c.Path, "securityContext") {
			out = append(out, c.Path)
		}
	}
	return out
}

// valueAt returns the rendered value written at path, and whether the plan
// writes it at all.
func valueAt(p Plan, path string) (string, bool) {
	for _, c := range p.Changes {
		if c.Path == path {
			return c.Value, true
		}
	}
	return "", false
}

// findingFor returns the reasons reported against a container.
func findingFor(p Plan, container string) []string {
	var out []string
	for _, f := range p.Findings {
		if f.Container == container {
			out = append(out, f.Reason)
		}
	}
	return out
}

// mentions reports whether any of the reasons contains substr.
func mentions(reasons []string, substr string) bool {
	return slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, substr) })
}

// AC-01, the securityContext half: a template missing everything yields
// exactly the four always-on fields at the correct levels, initContainers
// included, ephemeralContainers untouched, and no other securityContext field.
// Task 4 adds the resource half and asserts the complete path list.
func TestBuildAlwaysOnFields(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup", Image: "busybox"}},
		Containers:     []corev1.Container{{Name: "app", Image: "nginx"}},
		EphemeralContainers: []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "busybox"},
		}},
	}

	got := securityPaths(Build(pod, basic()))
	want := []string{
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.containers[app].securityContext.capabilities.drop",
		"spec.template.spec.initContainers[setup].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop",
		"spec.template.spec.securityContext.runAsNonRoot",
		"spec.template.spec.securityContext.seccompProfile.type",
	}
	if !slices.Equal(got, want) {
		t.Errorf("securityContext paths =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	// The paths are sorted, which is what makes the change hash stable.
	if !slices.IsSorted(paths(Build(pod, basic()))) {
		t.Error("changes are not sorted; the change hash would move between passes")
	}

	p := Build(pod, basic())
	for path, want := range map[string]string{
		"spec.template.spec.securityContext.runAsNonRoot":                             "true",
		"spec.template.spec.securityContext.seccompProfile.type":                      "RuntimeDefault",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation": "false",
		"spec.template.spec.containers[app].securityContext.capabilities.drop":        "[ALL]",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop":  "[ALL]",
	} {
		if got, ok := valueAt(p, path); !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", path, got, ok, want)
		}
	}

	// The JSON form must be the type the API server expects, not a string.
	for path, want := range map[string]any{
		"spec.template.spec.securityContext.runAsNonRoot":                            true,
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation": false,
	} {
		for _, c := range p.Changes {
			if c.Path == path && c.JSON != want {
				t.Errorf("%s JSON = %#v, want %#v", path, c.JSON, want)
			}
		}
	}

	// ephemeralContainers are added to running pods, not to templates, and are
	// never patched (BR-02, AC-01).
	for _, path := range paths(p) {
		if strings.Contains(path, "ephemeral") || strings.Contains(path, "debug") {
			t.Errorf("an ephemeral container reached the plan: %s", path)
		}
	}
}

// AC-04: readOnlyRootFilesystem appears in no plan unless requested, and in
// every eligible container when it is. No change ever names a limit.
func TestReadOnlyRootFilesystemIsOptIn(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup"}},
		Containers:     []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
	}

	off := Build(pod, basic())
	for _, path := range paths(off) {
		if strings.Contains(path, "readOnlyRootFilesystem") {
			t.Errorf("readOnlyRootFilesystem written without being requested: %s", path)
		}
	}

	policy := basic()
	policy.ReadOnlyRootFilesystem = true
	on := Build(pod, policy)
	for _, want := range []string{
		"spec.template.spec.containers[app].securityContext.readOnlyRootFilesystem",
		"spec.template.spec.containers[sidecar].securityContext.readOnlyRootFilesystem",
		"spec.template.spec.initContainers[setup].securityContext.readOnlyRootFilesystem",
	} {
		if v, ok := valueAt(on, want); !ok || v != "true" {
			t.Errorf("%s = %q (present %v), want true", want, v, ok)
		}
	}

	// No change, in either mode, ever names a limit (BR-03, AC-04).
	for _, p := range []Plan{off, on} {
		for _, path := range paths(p) {
			if strings.Contains(path, "limits") {
				t.Errorf("a limits path reached the plan: %s", path)
			}
		}
	}
}
```

Add the `requests` helper at the bottom of the same file; Task 4's tests use it too:

```go
// requests builds a ResourceList from quantity strings, panicking on a bad
// one — a test that cannot express its own input has no result to report.
func requests(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}
}
```

with `"k8s.io/apimachinery/pkg/api/resource"` in the import block.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/plan/ -run TestBuildAlwaysOnFields -v`
Expected: FAIL — `no required module provides package .../pkg/plan` / build failure, `undefined: Build`, `undefined: Policy`.

- [ ] **Step 3: Write the implementation**

Create `pkg/plan/plan.go`:

```go
// Package plan turns one workload template and one policy into the set of
// fields this tool would write into it, plus the findings it observed on the
// way. It is pure — no clients, no context, no clock — so the hard part of
// this feature is testable on its own (NFR-01, FR-02).
//
// Gaps are decided on effective values, not on what the template says, so the
// package models Kubernetes' own defaulting: pod to container precedence for
// securityContext, limits to requests for resources, and the namespace
// LimitRange for both. None of the three is observable in a template, so none
// can be skipped (BR-01).
package plan

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// templatePath is where a Deployment, StatefulSet and DaemonSet all carry the
// pod template. All three carry it at the same path, which is why all three
// are one code path (BR-04).
const templatePath = "spec.template.spec"

const (
	containersField     = "containers"
	initContainersField = "initContainers"
)

// Target identifies one workload. It is part of the change hash, so two
// workloads needing exactly the same fields never share a hash and an approval
// can never travel from one target to another (BR-07).
type Target struct {
	Namespace string
	Kind      string
	Name      string
}

func (t Target) String() string { return t.Kind + "/" + t.Namespace + "/" + t.Name }

// Change is one leaf field this tool would write.
type Change struct {
	// Path is the leaf path on the target object, with container lists keyed
	// by name rather than index:
	//
	//	spec.template.spec.securityContext.runAsNonRoot
	//	spec.template.spec.containers[app].resources.requests.cpu
	//
	// Leaf, not the shallowest path created. Recording "resources" rather than
	// "resources.requests.cpu" would make an undo delete a limits block a
	// human added afterwards, and that safety property is worth more than
	// avoiding a cosmetic empty object where a field is removed (BR-08).
	//
	// Keying by name rather than index also keeps an init container distinct
	// from a regular one and survives a reordered list.
	Path string
	// Value is Path's value rendered for the provenance annotation and the
	// change hash.
	Value string
	// JSON is the same value in the Go type the patch body needs, so the API
	// server is never handed the string "false" where it expects a bool.
	JSON any
}

// Finding is something observed and reported but never patched, with the
// reason (BR-04). Container is empty for a pod-level observation.
type Finding struct {
	Container string
	Reason    string
}

// Plan is one target's outcome. FR-02 names the return []Change; the findings
// come out of the same traversal and are the other half of BR-04, so they ride
// along rather than costing a second walk of the template.
type Plan struct {
	Changes  []Change
	Findings []Finding
}

// Policy is what the operator asked for, plus what the namespace already
// supplies.
type Policy struct {
	// Requests names the value to write into an absent request. The schema
	// requires both cpu and memory (FR-01).
	Requests corev1.ResourceList
	// ReadOnlyRootFilesystem is the one opt-in field (BR-02, D-06).
	ReadOnlyRootFilesystem bool
	// Coverage is what the namespace's LimitRanges already supply (BR-06).
	Coverage Coverage
}

// container pairs one container with the list it came from, so a path names
// the right list and an init container is never confused with a regular one.
type container struct {
	List string
	Spec *corev1.Container
}

// path returns the leaf path of field on this container.
func (c container) path(field string) string {
	return templatePath + "." + c.List + "[" + c.Spec.Name + "]." + field
}

// containers returns every container this tool may patch: both containers and
// initContainers, regular ones first, in template order. ephemeralContainers
// are excluded and are never read — they are added to running pods, not to
// templates (BR-02).
func containers(pod *corev1.PodSpec) []container {
	out := make([]container, 0, len(pod.Containers)+len(pod.InitContainers))
	for i := range pod.Containers {
		out = append(out, container{List: containersField, Spec: &pod.Containers[i]})
	}
	for i := range pod.InitContainers {
		out = append(out, container{List: initContainersField, Spec: &pod.InitContainers[i]})
	}
	return out
}

// Build returns the changes policy would make to pod and the findings it
// observed. The same function feeds the dry-run, the real patch and the
// rendered status, so a preview cannot diverge from what is applied (FR-02).
func Build(pod *corev1.PodSpec, policy Policy) Plan {
	var p Plan
	if pod == nil {
		return p
	}

	cs := containers(pod)

	// Pod level first: whether runAsNonRoot may be written at all depends on
	// every container in the pod, so the scan has to finish before the field
	// is decided (BR-02, AC-05).
	p.buildPod(pod, cs)

	for _, c := range cs {
		p.buildContainer(pod, c, policy)
	}

	// Sorted, and sorted here rather than at the call site: the change hash is
	// taken over this list, Go randomises map iteration, and an unsorted plan
	// would hash differently on most passes and invalidate every approval
	// (BR-07).
	slices.SortFunc(p.Changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	return p
}

// add records one change.
func (p *Plan) add(path, value string, jsonValue any) {
	p.Changes = append(p.Changes, Change{Path: path, Value: value, JSON: jsonValue})
}

// report records one finding.
func (p *Plan) report(container, format string, args ...any) {
	p.Findings = append(p.Findings, Finding{Container: container, Reason: fmt.Sprintf(format, args...)})
}

// buildPod decides the two pod-level fields.
//
// runAsNonRoot is written at pod level, so one container evidencing a need for
// root suppresses it for the entire pod. Reporting that container as a finding
// while writing the field anyway is the single most likely way this tool
// breaks a DaemonSet, and no dry-run refuses it: the failure is the kubelet's,
// at container creation (BR-02, AC-05).
//
// seccompProfile is written whatever the evidence says: it is one of the three
// fields that are still written when root is evidenced.
func (p *Plan) buildPod(pod *corev1.PodSpec, cs []container) {
	sc := pod.SecurityContext

	evidence := rootEvidence(pod, cs)
	p.Findings = append(p.Findings, evidence...)

	if len(evidence) == 0 && (sc == nil || sc.RunAsNonRoot == nil) &&
		reaches(cs, func(c *corev1.SecurityContext) bool { return c.RunAsNonRoot != nil }) {
		p.add(templatePath+".securityContext.runAsNonRoot", "true", true)
	}

	if (sc == nil || sc.SeccompProfile == nil) &&
		reaches(cs, func(c *corev1.SecurityContext) bool { return c.SeccompProfile != nil }) {
		p.add(templatePath+".securityContext.seccompProfile.type",
			string(corev1.SeccompProfileTypeRuntimeDefault),
			string(corev1.SeccompProfileTypeRuntimeDefault))
	}
}

// reaches reports whether at least one container still has no effective value
// for a pod-level field — that is, whether writing it at pod level would reach
// anything at all. A pod whose every container already declares the field has
// no gap: the container wins, so the pod-level write would change nothing
// (BR-01, AC-02). declared says whether a container's own securityContext
// carries the field.
func reaches(cs []container, declared func(*corev1.SecurityContext) bool) bool {
	for _, c := range cs {
		if c.Spec.SecurityContext == nil || !declared(c.Spec.SecurityContext) {
			return true
		}
	}
	return false
}

// rootEvidence returns a finding for every container that evidences a need for
// root. Evidence is an explicit privileged: true, or an effective runAsUser of
// 0 — the container's own if it sets one, the pod's otherwise. Both forms
// count because CNI agents, log shippers and node exporters are routinely
// privileged without ever declaring runAsUser: 0 (BR-02).
func rootEvidence(pod *corev1.PodSpec, cs []container) []Finding {
	var out []Finding
	for _, c := range cs {
		switch {
		case privileged(c.Spec):
			out = append(out, Finding{Container: c.Spec.Name, Reason: "container is explicitly privileged: " +
				"pod-level runAsNonRoot is not written, and this container's securityContext is left alone (BR-02)"})
		default:
			if uid, set := effectiveRunAsUser(pod, c.Spec); set && uid == 0 {
				out = append(out, Finding{Container: c.Spec.Name, Reason: "effective runAsUser is 0: " +
					"pod-level runAsNonRoot is not written for this pod (BR-02)"})
			}
		}
	}
	return out
}

// privileged reports whether a container is explicitly privileged.
func privileged(c *corev1.Container) bool {
	return c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged
}

// effectiveRunAsUser resolves runAsUser by pod to container precedence, the
// container winning. The second result is false when neither level sets one,
// in which case the image's own USER decides and the template is silent about
// it — which is exactly why runAsUser is never written (BR-02, D-05).
func effectiveRunAsUser(pod *corev1.PodSpec, c *corev1.Container) (int64, bool) {
	if c.SecurityContext != nil && c.SecurityContext.RunAsUser != nil {
		return *c.SecurityContext.RunAsUser, true
	}
	if pod.SecurityContext != nil && pod.SecurityContext.RunAsUser != nil {
		return *pod.SecurityContext.RunAsUser, true
	}
	return 0, false
}

// buildContainer decides the container-level fields for one container.
func (p *Plan) buildContainer(pod *corev1.PodSpec, c container, policy Policy) {
	if privileged(c.Spec) {
		// Already reported by rootEvidence, and left entirely alone. Dropping
		// a privileged container's capabilities would be theatre (BR-02), it
		// is listed under "reported as findings, never patched" (BR-04), and
		// the API server refuses allowPrivilegeEscalation: false beside
		// privileged: true outright — so writing it would fail this target's
		// whole dry-run and take its sibling containers with it.
		return
	}

	sc := c.Spec.SecurityContext

	switch {
	case sc == nil || sc.AllowPrivilegeEscalation == nil:
		p.add(c.path("securityContext.allowPrivilegeEscalation"), "false", false)
	case *sc.AllowPrivilegeEscalation:
		p.report(c.Spec.Name, "container declares allowPrivilegeEscalation: true; reported, not overruled (BR-01)")
	}

	switch {
	case sc == nil || sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0:
		// A container that also carries capabilities.add keeps it: Kubernetes
		// applies drop before add, so merging drop: [ALL] beside an existing
		// add leaves exactly the capabilities that were asked for.
		p.add(c.path("securityContext.capabilities.drop"), "[ALL]", []any{"ALL"})
	case !slices.Contains(sc.Capabilities.Drop, corev1.Capability("ALL")):
		p.report(c.Spec.Name, "capabilities.drop is already set and does not drop ALL; reported, not overruled (BR-01)")
	}

	if policy.ReadOnlyRootFilesystem {
		switch {
		case sc == nil || sc.ReadOnlyRootFilesystem == nil:
			p.add(c.path("securityContext.readOnlyRootFilesystem"), "true", true)
		case !*sc.ReadOnlyRootFilesystem:
			p.report(c.Spec.Name, "container declares readOnlyRootFilesystem: false; reported, not overruled (BR-01)")
		}
	}

	// A container overriding a pod-level field to a weaker value is reported,
	// because the container wins (FR-02, AC-02). Neither of these is a gap:
	// the value is present, and BR-01 never overwrites one.
	if sc != nil && sc.RunAsNonRoot != nil && !*sc.RunAsNonRoot {
		p.report(c.Spec.Name, "container declares runAsNonRoot: false; reported, not overruled (BR-01)")
	}
	if sc != nil && sc.SeccompProfile != nil && sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		p.report(c.Spec.Name, "container declares seccompProfile.type: %s; reported, not overruled (BR-01)", sc.SeccompProfile.Type)
	}

	p.buildResources(c, policy)
}
```

`buildResources` does not exist yet — Task 4 writes it. To keep this task compiling on its own, add a stub at the bottom of `plan.go` and **replace it in Task 4, Step 3**:

```go
// buildResources is written in Task 4. The stub keeps Task 3 compiling; it
// must be replaced, not kept.
func (p *Plan) buildResources(c container, policy Policy) {}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/plan/ -run 'TestBuildAlwaysOnFields|TestReadOnly' -v`
Expected: PASS, both. The tests in this task assert only the securityContext half of the plan, so the `buildResources` stub does not make them fail — Task 4 replaces the stub and adds the complete-path assertion AC-01 asks for.

- [ ] **Step 5: Commit**

```bash
git add pkg/plan
git commit -m "feat(plan): securityContext gaps, pod-to-container precedence, root evidence

Pod-level fields are a gap only where writing them would reach a container
that has no effective value of its own. Root evidence suppresses pod-level
runAsNonRoot for the whole pod while the other fields are still written, and
an explicitly privileged container is reported and left entirely alone.

Resource requests are stubbed; Task 4 writes them."
```

- [ ] **Step 6: Write the precedence test (AC-02)**

Append to `pkg/plan/plan_test.go`:

```go
func ptr[T any](v T) *T { return &v }

// AC-02: securityContext precedence. The field you read is not the value that
// applies, and the container wins.
func TestSecurityContextPrecedence(t *testing.T) {
	t.Run("pod true with a container false is a finding, not hardened", func(t *testing.T) {
		pod := &corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true)},
			Containers:      []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(false)}}},
		}
		p := Build(pod, basic())

		// Pod level already carries the field, so there is no gap to fill
		// there; and the container's own false is never overwritten (BR-01).
		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot rewritten over a value that is already present")
		}
		if !mentions(findingFor(p, "app"), "runAsNonRoot: false") {
			t.Errorf("findings = %v, want the container's false reported", findingFor(p, "app"))
		}
	})

	t.Run("container true with nothing at pod level is left alone", func(t *testing.T) {
		pod := &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true)}}},
		}
		p := Build(pod, basic())
		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot written where every container already declares it: the write reaches nothing")
		}
		if len(findingFor(p, "app")) != 0 {
			t.Errorf("findings = %v, want none: the container is already hardened", findingFor(p, "app"))
		}
	})

	t.Run("one of two containers declaring it still leaves a gap", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true)}},
			{Name: "sidecar"},
		}}
		p := Build(pod, basic())
		if v, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); !ok || v != "true" {
			t.Error("pod-level runAsNonRoot not written where a container has no effective value")
		}
	})

	t.Run("seccompProfile follows the same rule", func(t *testing.T) {
		declared := &corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			SecurityContext: &corev1.SecurityContext{
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		}}}
		if _, ok := valueAt(Build(declared, basic()), "spec.template.spec.securityContext.seccompProfile.type"); ok {
			t.Error("pod-level seccompProfile written where every container already declares it")
		}

		unconfined := &corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			SecurityContext: &corev1.SecurityContext{
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
			},
		}}}
		p := Build(unconfined, basic())
		if _, ok := valueAt(p, "spec.template.spec.securityContext.seccompProfile.type"); ok {
			t.Error("pod-level seccompProfile written over a container that declares Unconfined; the container wins")
		}
		if !mentions(findingFor(p, "app"), "Unconfined") {
			t.Errorf("findings = %v, want the weaker profile reported", findingFor(p, "app"))
		}
	})

	t.Run("an explicit weaker container value is never overwritten", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr(true),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
			},
		}}}
		p := Build(pod, basic())
		for _, path := range []string{
			"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
			"spec.template.spec.containers[app].securityContext.capabilities.drop",
		} {
			if _, ok := valueAt(p, path); ok {
				t.Errorf("%s overwritten; BR-01 never corrects a value that is already present", path)
			}
		}
		reasons := findingFor(p, "app")
		if !mentions(reasons, "allowPrivilegeEscalation: true") || !mentions(reasons, "does not drop ALL") {
			t.Errorf("findings = %v, want both weaker values reported", reasons)
		}
	})

	t.Run("capabilities with only add still leaves drop a gap", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{{
			Name:            "app",
			SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}},
		}}}
		p := Build(pod, basic())
		if v, ok := valueAt(p, "spec.template.spec.containers[app].securityContext.capabilities.drop"); !ok || v != "[ALL]" {
			t.Error("capabilities.drop not written where only add is set; drop is absent and is a gap")
		}
	})
}
```

- [ ] **Step 7: Run it**

Run: `go test ./pkg/plan/ -run TestSecurityContextPrecedence -v`
Expected: PASS, all six sub-tests.

- [ ] **Step 8: Write the root-evidence test (AC-05)**

This is the test that stands between this tool and a dead DaemonSet. Append to `pkg/plan/plan_test.go`:

```go
// AC-05: a privileged container, and an effective runAsUser of 0, each
// suppress pod-level runAsNonRoot for the whole pod and yield a finding — and
// the other three fields are still written.
//
// runAsNonRoot is a pod-level field, so a single container needing root
// poisons it for every container in the pod. Reporting that container as a
// finding, which reads as "left alone", while still imposing runAsNonRoot on
// it produces CreateContainerConfigError at the kubelet, and no dry-run
// refuses it.
func TestRootEvidenceSuppressesRunAsNonRoot(t *testing.T) {
	t.Run("a privileged container", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "agent", SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)}},
			{Name: "app"},
		}}
		p := Build(pod, basic())

		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot written beside a privileged container; this is the DaemonSet killer")
		}
		// The other three are still written.
		if _, ok := valueAt(p, "spec.template.spec.securityContext.seccompProfile.type"); !ok {
			t.Error("pod-level seccompProfile suppressed; only runAsNonRoot is (BR-02)")
		}
		for _, path := range []string{
			"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
			"spec.template.spec.containers[app].securityContext.capabilities.drop",
		} {
			if _, ok := valueAt(p, path); !ok {
				t.Errorf("%s suppressed; the unprivileged container is still hardened", path)
			}
		}
		// The privileged container itself is reported and untouched: the API
		// server refuses allowPrivilegeEscalation: false beside privileged: true.
		for _, path := range paths(p) {
			if strings.Contains(path, "containers[agent]") {
				t.Errorf("the privileged container was patched: %s", path)
			}
		}
		if !mentions(findingFor(p, "agent"), "privileged") {
			t.Errorf("findings = %v, want the privileged container reported", findingFor(p, "agent"))
		}
	})

	t.Run("a container-level runAsUser of 0", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "root", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(0))}},
			{Name: "app"},
		}}
		p := Build(pod, basic())

		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot written beside an effective runAsUser of 0")
		}
		if !mentions(findingFor(p, "root"), "runAsUser is 0") {
			t.Errorf("findings = %v, want the root container reported", findingFor(p, "root"))
		}
		// Unlike a privileged container, this one is not exempt from the
		// container-level fields: BR-04 names privileged containers only.
		for _, path := range []string{
			"spec.template.spec.containers[root].securityContext.allowPrivilegeEscalation",
			"spec.template.spec.containers[root].securityContext.capabilities.drop",
			"spec.template.spec.containers[root].resources.requests.cpu",
		} {
			if _, ok := valueAt(p, path); !ok {
				t.Errorf("%s suppressed; only pod-level runAsNonRoot is (BR-02)", path)
			}
		}
	})

	t.Run("a pod-level runAsUser of 0 inherited by a container", func(t *testing.T) {
		pod := &corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr(int64(0))},
			Containers:      []corev1.Container{{Name: "app"}},
		}
		if _, ok := valueAt(Build(pod, basic()), "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("the effective runAsUser is inherited from the pod and is 0; runAsNonRoot must be suppressed")
		}
	})

	t.Run("a container overriding a pod-level root to non-root is not evidence", func(t *testing.T) {
		pod := &corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr(int64(0))},
			Containers: []corev1.Container{
				{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(65532))}},
			},
		}
		if _, ok := valueAt(Build(pod, basic()), "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("no container has an effective runAsUser of 0, so runAsNonRoot must still be written")
		}
	})

	t.Run("an init container evidences root for the whole pod", func(t *testing.T) {
		pod := &corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "chown", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(0))}}},
			Containers:     []corev1.Container{{Name: "app"}},
		}
		p := Build(pod, basic())
		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("an init container running as root still blocks the pod from starting under runAsNonRoot")
		}
		if !mentions(findingFor(p, "chown"), "runAsUser is 0") {
			t.Errorf("findings = %v, want the init container reported", findingFor(p, "chown"))
		}
	})

	t.Run("no evidence means the field is written", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(65532))}},
		}}
		if v, ok := valueAt(Build(pod, basic()), "spec.template.spec.securityContext.runAsNonRoot"); !ok || v != "true" {
			t.Error("runAsNonRoot must be written where nothing evidences a need for root")
		}
	})
}
```

- [ ] **Step 9: Run it**

Run: `go test ./pkg/plan/ -run TestRootEvidence -v`
Expected: PASS, all six sub-tests.

If "a privileged container" fails on `pod-level runAsNonRoot written beside a privileged container`, `buildPod` is deciding `runAsNonRoot` before the container scan has finished. The scan must complete first — that ordering is the whole rule.

- [ ] **Step 10: Commit**

```bash
git add pkg/plan/plan_test.go
git commit -m "test(plan): securityContext precedence and root evidence for AC-02 and AC-05

Pins the two rules where the field read is not the value that applies:
the container wins over the pod, and one container needing root suppresses
pod-level runAsNonRoot for every container in the pod."
```

---

### Task 4: The plan — effective requests and LimitRange (BR-03, BR-06, AC-03, AC-06)

AC-03 is the sharpest thing in this spec. Nothing in a workload's YAML shows that a `limits`-only container has an effective request equal to its limit, because the defaulting happens when the **Pod** is created and the template a tool reads is silent about it. Measured on a live cluster: a Deployment declaring `limits: {cpu: 500m, memory: 1Gi}` and no requests runs as QoS `Guaranteed` with `requests == limits`; writing `requests: {cpu: 10m, memory: 32Mi}` into it cuts the CPU reservation 50×, the memory reservation 32×, and demotes it to `Burstable`. The tool would degrade the workload it was asked to protect, pass its own dry-run, and report success.

AC-06 is the same trap one scope up, sourced from the namespace. A LimitRange declaring only `default: {memory: 512Mi}` gives every container an effective request of 512Mi — reading `defaultRequest` alone reports a gap that does not exist.

The min/max half of BR-06 is a correctness rule, not an optimisation: LimitRange is enforced at **pod** admission, so a template whose pods it rejects patches cleanly and then stalls the rollout, and the API server's dry-run against the workload object cannot catch it.

**Files:**

- Create: `pkg/plan/resources.go`
- Modify: `pkg/plan/plan.go` (delete the `buildResources` stub from Task 3)
- Test: `pkg/plan/resources_test.go`

**Interfaces:**

- Consumes: `container`, `Plan`, `Policy`, `(*Plan).add`, `(*Plan).report` (Task 3).
- Produces:
  - `type plan.Coverage struct` with `func (Coverage) Request(corev1.ResourceName) string` and `func (Coverage) Limit(corev1.ResourceName) string`
  - `func plan.Cover(items []corev1.LimitRange, requests corev1.ResourceList) (Coverage, string)` — the second result is a rejection reason, `""` when the bounds admit every value this tool would write
  - `func (p *Plan) buildResources(c container, policy Policy)` — replaces Task 3's stub

- [ ] **Step 1: Write the failing effective-requests test (AC-03)**

Create `pkg/plan/resources_test.go`:

```go
package plan

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// AC-03, the three-row truth table of BR-01. The middle row is the one that
// matters: a container with limits set and requests absent has an effective
// request equal to the limit, so there is no gap, and filling it would cut the
// pod's reservation and demote it from Guaranteed to Burstable.
func TestEffectiveRequests(t *testing.T) {
	cases := []struct {
		name      string
		resources corev1.ResourceRequirements
		gaps      []string // paths under containers[app]
		finding   string   // substring, "" for none
	}{
		{
			name:      "nothing declared: effective request is none, so both are gaps",
			resources: corev1.ResourceRequirements{},
			gaps:      []string{"resources.requests.cpu", "resources.requests.memory"},
			finding:   "",
		},
		{
			name: "memory limit and no requests: effective memory request is 1Gi, so no memory gap",
			resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
			gaps:    []string{"resources.requests.cpu"},
			finding: "memory request defaulted from limit",
		},
		{
			name: "both limits and no requests: no gap at all",
			resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			gaps:    nil,
			finding: "request defaulted from limit",
		},
		{
			name: "explicit requests: untouched",
			resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
			gaps:    nil,
			finding: "",
		},
		{
			name: "one explicit request, one absent",
			resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
			},
			gaps:    []string{"resources.requests.cpu"},
			finding: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: c.resources}}}
			p := Build(pod, basic())

			for _, want := range []string{"resources.requests.cpu", "resources.requests.memory"} {
				full := "spec.template.spec.containers[app]." + want
				_, written := valueAt(p, full)
				shouldWrite := false
				for _, g := range c.gaps {
					if g == want {
						shouldWrite = true
					}
				}
				if written != shouldWrite {
					t.Errorf("%s written = %v, want %v", full, written, shouldWrite)
				}
			}

			reasons := findingFor(p, "app")
			if c.finding != "" && !mentions(reasons, c.finding) {
				t.Errorf("findings = %v, want one mentioning %q", reasons, c.finding)
			}
			if c.finding == "" && mentions(reasons, "defaulted from limit") {
				t.Errorf("findings = %v, want no defaulted-from-limit finding", reasons)
			}
		})
	}
}

// BR-03: an absent limit is a finding whose remedy names the mechanism that
// exists for it. Limits are never written, whatever else is true.
func TestAbsentLimitIsAFindingNotAChange(t *testing.T) {
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	p := Build(pod, basic())

	if !mentions(findingFor(p, "app"), "LimitRange") {
		t.Errorf("findings = %v, want the absent limits reported with LimitRange as the remedy", findingFor(p, "app"))
	}
	for _, c := range p.Changes {
		if strings.Contains(c.Path, "limits") {
			t.Errorf("a limit reached the plan: %s = %s", c.Path, c.Value)
		}
	}
}

// The value written is the quantity's canonical form, not the operator's
// literal spelling, because that is what the change hash is taken over. 1000m
// and 1 are one approval, not two.
func TestRequestValueIsCanonical(t *testing.T) {
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	p := Build(pod, Policy{Requests: requests("1000m", "32Mi")})

	if v, _ := valueAt(p, "spec.template.spec.containers[app].resources.requests.cpu"); v != "1" {
		t.Errorf("cpu = %q, want the canonical %q", v, "1")
	}
}

// AC-01, completed: exactly the four always-on fields plus the two requests,
// at the correct levels, initContainers included, ephemeralContainers
// untouched, and no other field. Task 3 pinned the securityContext half
// against the buildResources stub; this is the whole list.
func TestBuildEmitsExactlyTheExpectedPaths(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup", Image: "busybox"}},
		Containers:     []corev1.Container{{Name: "app", Image: "nginx"}},
		EphemeralContainers: []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "busybox"},
		}},
	}

	got := paths(Build(pod, basic()))
	want := []string{
		"spec.template.spec.containers[app].resources.requests.cpu",
		"spec.template.spec.containers[app].resources.requests.memory",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.containers[app].securityContext.capabilities.drop",
		"spec.template.spec.initContainers[setup].resources.requests.cpu",
		"spec.template.spec.initContainers[setup].resources.requests.memory",
		"spec.template.spec.initContainers[setup].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop",
		"spec.template.spec.securityContext.runAsNonRoot",
		"spec.template.spec.securityContext.seccompProfile.type",
	}
	if !slicesEqual(got, want) {
		t.Errorf("paths =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, path := range got {
		if strings.Contains(path, "debug") || strings.Contains(path, "ephemeral") {
			t.Errorf("an ephemeral container reached the plan: %s", path)
		}
	}
}

// slicesEqual keeps this file free of a slices import it needs nowhere else.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/plan/ -run 'TestEffectiveRequests|TestAbsentLimit|TestRequestValueIsCanonical|TestBuildEmitsExactly' -v`
Expected: FAIL — every case reporting `written = false, want true`, because `buildResources` is still Task 3's empty stub.

- [ ] **Step 3: Write the implementation**

Delete the `buildResources` stub from the bottom of `pkg/plan/plan.go`, then create `pkg/plan/resources.go`:

```go
package plan

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Coverage is what a namespace's Container-scoped LimitRanges already supply
// for the resources this tool would fill. If a LimitRange supplies a request,
// the API server injects it into every pod it admits, so writing it into the
// template would restart every pod in the namespace to change nothing (BR-06).
type Coverage struct {
	request map[corev1.ResourceName]string
	limit   map[corev1.ResourceName]string
}

// Request names the LimitRange supplying a default request for r, or "".
func (c Coverage) Request(r corev1.ResourceName) string { return c.request[r] }

// Limit names the LimitRange supplying a default limit for r, or "".
func (c Coverage) Limit(r corev1.ResourceName) string { return c.limit[r] }

// Cover summarises the Container-scoped LimitRanges of one namespace against
// the values the operator asked for. The second result is a rejection reason
// when min or max excludes a value this tool would write, and "" otherwise.
//
// That check is a correctness rule, not an optimisation: LimitRange is
// enforced at pod admission, so a template whose pods it rejects patches
// cleanly and then stalls the rollout, and the API server's dry-run against
// the workload object cannot catch it (BR-06).
//
// A namespace may hold several LimitRanges and one LimitRange several
// Container-scoped items, and the API server applies all of them — so coverage
// is their union and the bounds are their intersection.
//
// ResourceQuota is deliberately not checked. A quota constraining
// requests.<resource> requires every incoming container to request it
// explicitly, so workloads with an absent request cannot already be running in
// such a namespace; and where a LimitRange supplies the default, the rule
// above has already removed the gap (BR-06, D-15).
func Cover(items []corev1.LimitRange, requests corev1.ResourceList) (Coverage, string) {
	cov := Coverage{
		request: map[corev1.ResourceName]string{},
		limit:   map[corev1.ResourceName]string{},
	}

	// Sorted by name, so a namespace whose LimitRanges both supply the same
	// resource always names the same one and the published plan does not flap
	// between passes.
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b corev1.LimitRange) int { return strings.Compare(a.Name, b.Name) })

	names := slices.Sorted(maps.Keys(requests))

	// First pass: what is supplied. It has to finish before the bounds are
	// checked, because a value this tool will not write is not a value the
	// bounds apply to — and the LimitRange supplying it may be a different one
	// from the LimitRange setting the bound.
	for i := range sorted {
		for _, item := range containerItems(&sorted[i]) {
			for _, r := range names {
				// Both defaultRequest and default supply a request. Where
				// defaultRequest is omitted Kubernetes uses default for it,
				// and even by the plainer route a defaulted limit is copied to
				// the request when the Pod is defaulted. Reading defaultRequest
				// alone reports a gap that does not exist — BR-01's mistake,
				// one scope up (BR-06, AC-06).
				_, hasDefaultRequest := item.DefaultRequest[r]
				_, hasDefault := item.Default[r]
				if hasDefaultRequest || hasDefault {
					claim(cov.request, r, sorted[i].Name)
				}
				if hasDefault {
					claim(cov.limit, r, sorted[i].Name)
				}
			}
		}
	}

	// Second pass: the bounds, for the resources this tool would actually
	// write. maxLimitRequestRatio needs no check: it constrains limit over
	// request, and BR-01 never writes a request into a container that already
	// has a limit for that resource, so this tool cannot move the ratio.
	for i := range sorted {
		for _, item := range containerItems(&sorted[i]) {
			for _, r := range names {
				if cov.request[r] != "" {
					continue
				}
				want := requests[r]
				if lo, ok := item.Min[r]; ok && want.Cmp(lo) < 0 {
					return cov, fmt.Sprintf(
						"LimitRange %q sets a Container min of %s=%s, which excludes the requested %s",
						sorted[i].Name, r, lo.String(), want.String())
				}
				if hi, ok := item.Max[r]; ok && want.Cmp(hi) > 0 {
					return cov, fmt.Sprintf(
						"LimitRange %q sets a Container max of %s=%s, which excludes the requested %s",
						sorted[i].Name, r, hi.String(), want.String())
				}
			}
		}
	}
	return cov, ""
}

// containerItems returns the Container-scoped items of one LimitRange. Pod-
// and PVC-scoped items constrain something else and default nothing into a
// container, so they are ignored (BR-06).
func containerItems(lr *corev1.LimitRange) []corev1.LimitRangeItem {
	var out []corev1.LimitRangeItem
	for i := range lr.Spec.Limits {
		if lr.Spec.Limits[i].Type == corev1.LimitTypeContainer {
			out = append(out, lr.Spec.Limits[i])
		}
	}
	return out
}

// claim records name for r only if nothing has claimed it yet, so the
// lowest-named LimitRange wins and the report is stable.
func claim(m map[corev1.ResourceName]string, r corev1.ResourceName, name string) {
	if _, ok := m[r]; !ok {
		m[r] = name
	}
}

// buildResources decides the requests for one container.
//
// The effective request is what actually applies once Kubernetes has defaulted
// the Pod, and it is not visible in the template. A container with limits set
// and requests absent has an effective request equal to the limit; filling it
// would cut the reservation and demote the pod from Guaranteed to Burstable —
// the exact harm this feature exists to avoid, performed by the feature itself
// (BR-01, AC-03).
//
// Limits are never written. Filling requests moves a container out of the
// BestEffort class, which is the first thing evicted under node pressure; being
// wrong about a request costs scheduling position and shows up in
// `kubectl describe node` rather than in a pager. A memory limit that is too
// small kills the container after a rollout that completed green (BR-03, D-07).
func (p *Plan) buildResources(c container, policy Policy) {
	var absentLimits []string

	// Sorted, so the plan and therefore the change hash do not depend on map
	// iteration order (BR-07).
	for _, r := range slices.Sorted(maps.Keys(policy.Requests)) {
		_, hasRequest := c.Spec.Resources.Requests[r]
		_, hasLimit := c.Spec.Resources.Limits[r]

		if !hasLimit && policy.Coverage.Limit(r) == "" {
			absentLimits = append(absentLimits, string(r))
		}

		switch {
		case hasRequest:
			// Already present. Never overwrite, never lower, never correct a
			// value that is already there (BR-01, D-04).
		case hasLimit:
			p.report(c.Spec.Name, "%s request defaulted from limit; no gap, and filling it would cut the reservation (BR-01)", r)
		case policy.Coverage.Request(r) != "":
			p.report(c.Spec.Name, "%s request covered by LimitRange %q; no gap, and writing it would restart every pod to change nothing (BR-06)",
				r, policy.Coverage.Request(r))
		default:
			// A local copy: Quantity.String has a pointer receiver, so a map
			// value cannot be rendered in place.
			want := policy.Requests[r]
			p.add(c.path("resources.requests."+string(r)), want.String(), want.String())
		}
	}

	if len(absentLimits) > 0 {
		p.report(c.Spec.Name, "no %s limit; limits are never written by this tool — add a LimitRange to the namespace (BR-03, D-07)",
			strings.Join(absentLimits, " or "))
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/plan/ -v`
Expected: PASS everywhere, including `TestBuildAlwaysOnFields`, which Task 3 left failing on the four request paths.

- [ ] **Step 5: Commit**

```bash
git add pkg/plan
git commit -m "feat(plan): effective requests and LimitRange coverage

A container with limits and no requests has an effective request equal to
the limit: no gap, a finding. Filling it would cut the reservation and demote
the pod from Guaranteed to Burstable. A LimitRange default supplies the
request where defaultRequest is omitted, and min/max excluding a requested
value rejects the namespace, because LimitRange is enforced at pod admission
and the workload dry-run cannot see it."
```

- [ ] **Step 6: Write the failing LimitRange test (AC-06)**

Add `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"` to `pkg/plan/resources_test.go`'s import block — Step 1 deliberately left it out, because an unused import is a build failure rather than the test failure Step 2 is looking for. Then append:

```go
// limitRange builds a LimitRange holding one Container-scoped item.
func limitRange(name string, item corev1.LimitRangeItem) corev1.LimitRange {
	item.Type = corev1.LimitTypeContainer
	return corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{item}},
	}
}

// AC-06: a defaultRequest covering memory, and a default with no
// defaultRequest, each report the memory gap as covered and omit it from the
// plan; a min/max excluding the requested values rejects the namespace.
func TestLimitRangeCoverage(t *testing.T) {
	want := requests("10m", "32Mi")

	t.Run("defaultRequest covers memory", func(t *testing.T) {
		cov, why := Cover([]corev1.LimitRange{limitRange("defaults", corev1.LimitRangeItem{
			DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		})}, want)
		if why != "" {
			t.Fatalf("rejected: %s", why)
		}
		if cov.Request(corev1.ResourceMemory) != "defaults" {
			t.Errorf("memory request coverage = %q, want defaults", cov.Request(corev1.ResourceMemory))
		}
		if cov.Request(corev1.ResourceCPU) != "" {
			t.Errorf("cpu request coverage = %q, want none", cov.Request(corev1.ResourceCPU))
		}
		assertCovered(t, cov, corev1.ResourceMemory)
	})

	t.Run("default with no defaultRequest also covers memory", func(t *testing.T) {
		// Kubernetes uses default for defaultRequest when the latter is
		// omitted, so a LimitRange declaring only default: {memory: 512Mi}
		// gives every container an effective request of 512Mi. Reading
		// defaultRequest alone reports a gap that does not exist.
		cov, why := Cover([]corev1.LimitRange{limitRange("defaults", corev1.LimitRangeItem{
			Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		})}, want)
		if why != "" {
			t.Fatalf("rejected: %s", why)
		}
		if cov.Request(corev1.ResourceMemory) != "defaults" {
			t.Errorf("memory request coverage = %q, want defaults: default supplies defaultRequest (BR-06)",
				cov.Request(corev1.ResourceMemory))
		}
		if cov.Limit(corev1.ResourceMemory) != "defaults" {
			t.Errorf("memory limit coverage = %q, want defaults", cov.Limit(corev1.ResourceMemory))
		}
		assertCovered(t, cov, corev1.ResourceMemory)
	})

	t.Run("min excluding the requested cpu rejects the namespace", func(t *testing.T) {
		_, why := Cover([]corev1.LimitRange{limitRange("bounds", corev1.LimitRangeItem{
			Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		})}, want)
		if why == "" {
			t.Fatal("accepted a min of 100m against a requested 10m")
		}
		for _, substr := range []string{"bounds", "min", "100m", "10m"} {
			if !strings.Contains(why, substr) {
				t.Errorf("reason = %q, want it to mention %q", why, substr)
			}
		}
	})

	t.Run("max excluding the requested memory rejects the namespace", func(t *testing.T) {
		_, why := Cover([]corev1.LimitRange{limitRange("bounds", corev1.LimitRangeItem{
			Max: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")},
		})}, want)
		if why == "" {
			t.Fatal("accepted a max of 16Mi against a requested 32Mi")
		}
		if !strings.Contains(why, "max") || !strings.Contains(why, "16Mi") {
			t.Errorf("reason = %q, want the bound in the message", why)
		}
	})

	t.Run("bounds on a resource the LimitRange itself supplies do not apply", func(t *testing.T) {
		// This tool will not write cpu here, so its own value is never
		// validated against the bound: the API server injects the
		// LimitRange's default instead (BR-06).
		_, why := Cover([]corev1.LimitRange{limitRange("both", corev1.LimitRangeItem{
			DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			Min:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		})}, want)
		if why != "" {
			t.Errorf("rejected: %s — the bound applies to a resource this tool does not write", why)
		}
	})

	t.Run("a Pod-scoped item defaults nothing into a container", func(t *testing.T) {
		podScoped := corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-scoped"},
			Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypePod,
				DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
				Min:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}}},
		}
		cov, why := Cover([]corev1.LimitRange{podScoped}, want)
		if why != "" {
			t.Errorf("rejected by a Pod-scoped bound: %s", why)
		}
		if cov.Request(corev1.ResourceMemory) != "" {
			t.Error("a Pod-scoped defaultRequest was treated as container coverage")
		}
	})

	t.Run("no LimitRange at all", func(t *testing.T) {
		cov, why := Cover(nil, want)
		if why != "" {
			t.Errorf("rejected with no LimitRange: %s", why)
		}
		if cov.Request(corev1.ResourceCPU) != "" || cov.Limit(corev1.ResourceMemory) != "" {
			t.Error("coverage claimed with no LimitRange")
		}
	})
}

// assertCovered checks that Build omits the covered resource from the plan and
// reports it instead — the half of AC-06 that lives in the plan rather than in
// Cover.
func assertCovered(t *testing.T, cov Coverage, r corev1.ResourceName) {
	t.Helper()
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	p := Build(pod, Policy{Requests: requests("10m", "32Mi"), Coverage: cov})

	path := "spec.template.spec.containers[app].resources.requests." + string(r)
	if _, ok := valueAt(p, path); ok {
		t.Errorf("%s written although the LimitRange already supplies it", path)
	}
	if !mentions(findingFor(p, "app"), "covered by LimitRange") {
		t.Errorf("findings = %v, want the gap reported as covered", findingFor(p, "app"))
	}
}
```

- [ ] **Step 7: Run it**

Run: `go test ./pkg/plan/ -run TestLimitRangeCoverage -v`
Expected: PASS, all seven sub-tests.

If "default with no defaultRequest also covers memory" fails, `Cover` reads `DefaultRequest` only. That is the gap-that-does-not-exist bug, and it makes the tool restart every pod in the namespace to write a value the API server was already injecting.

- [ ] **Step 8: Write the several-LimitRanges test (Review Focus 2)**

Append to `pkg/plan/resources_test.go`:

```go
// Review Focus 2: BR-06 says "a Container-scoped LimitRange" in the singular,
// but a namespace may hold any number and one LimitRange may hold several
// Container-scoped items. The API server applies all of them, so coverage is
// their union and the bounds are their intersection. Reading only the first
// either reports a gap that does not exist or accepts a value the API server
// will refuse at pod admission — which no dry-run catches.
func TestSeveralLimitRanges(t *testing.T) {
	want := requests("10m", "32Mi")

	t.Run("coverage is the union across LimitRanges", func(t *testing.T) {
		cov, why := Cover([]corev1.LimitRange{
			limitRange("zeta-memory", corev1.LimitRangeItem{
				Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			}),
			limitRange("alpha-cpu", corev1.LimitRangeItem{
				DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			}),
		}, want)
		if why != "" {
			t.Fatalf("rejected: %s", why)
		}
		if cov.Request(corev1.ResourceCPU) != "alpha-cpu" {
			t.Errorf("cpu coverage = %q, want alpha-cpu", cov.Request(corev1.ResourceCPU))
		}
		if cov.Request(corev1.ResourceMemory) != "zeta-memory" {
			t.Errorf("memory coverage = %q, want zeta-memory", cov.Request(corev1.ResourceMemory))
		}
	})

	t.Run("a bound in the second LimitRange still rejects", func(t *testing.T) {
		_, why := Cover([]corev1.LimitRange{
			limitRange("aaa-harmless", corev1.LimitRangeItem{
				Max: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			}),
			limitRange("zzz-strict", corev1.LimitRangeItem{
				Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}),
		}, want)
		if why == "" {
			t.Fatal("a min in the second LimitRange was not applied")
		}
		if !strings.Contains(why, "zzz-strict") {
			t.Errorf("reason = %q, want it to name the LimitRange that excluded the value", why)
		}
	})

	t.Run("several Container-scoped items in one LimitRange", func(t *testing.T) {
		multi := corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{Name: "multi"},
			Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{
				{Type: corev1.LimitTypePod, Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
				{Type: corev1.LimitTypeContainer, Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}},
				{Type: corev1.LimitTypeContainer, Max: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m")}},
			}},
		}
		cov, why := Cover([]corev1.LimitRange{multi}, want)
		if cov.Request(corev1.ResourceMemory) != "multi" {
			t.Errorf("memory coverage = %q, want multi: the second item supplies it", cov.Request(corev1.ResourceMemory))
		}
		if why == "" {
			t.Fatal("the third item's max of 5m did not reject a requested 10m")
		}
		if !strings.Contains(why, "5m") {
			t.Errorf("reason = %q, want the third item's bound", why)
		}
	})

	t.Run("coverage from one LimitRange disarms a bound in another", func(t *testing.T) {
		// The union has to be computed before the bounds are checked: the
		// LimitRange supplying the request is not the one setting the bound.
		_, why := Cover([]corev1.LimitRange{
			limitRange("aaa-bound", corev1.LimitRangeItem{
				Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}),
			limitRange("zzz-default", corev1.LimitRangeItem{
				DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			}),
		}, want)
		if why != "" {
			t.Errorf("rejected: %s — cpu is supplied by zzz-default, so this tool never writes it and the bound does not apply", why)
		}
	})
}
```

- [ ] **Step 9: Run it and check package coverage**

Run: `go test ./pkg/plan/ -race -cover -v`
Expected: PASS, coverage ≥90%.

If "coverage from one LimitRange disarms a bound in another" fails, `Cover` checks bounds in the same loop that collects coverage, so the LimitRange sorted first decides before the later one has been read. The two passes are not an accident.

- [ ] **Step 10: Commit**

```bash
git add pkg/plan/resources_test.go
git commit -m "test(plan): LimitRange coverage and bounds for AC-06

Adds the several-LimitRanges case BR-06's singular phrasing does not name:
coverage is the union, the bounds are the intersection, and a request
supplied by one LimitRange disarms a bound set by another."
```

---

### Task 5: Change hash, patch body and provenance (BR-07, BR-08, FR-02, AC-11)

Three renderings of one list of changes, and they must not be able to disagree. The canonical serialisation is what is hashed; the provenance annotation is the same lines without the identity; the patch body is built by walking the same paths. One path format, interpreted in one place.

**The canonical form, stated precisely** — BR-07 requires a definition, and an implementation that drifts from it silently invalidates every approval:

```
<Kind>/<namespace>/<name>\n
<path>=<value>\n          (one per change)
...
```

- The first line is the target's identity, `Kind/namespace/name`. It is in the hash so that two workloads needing exactly the same fields never share a hash and an approval cannot travel from one target to another.
- Each subsequent line is one change's leaf path, `=`, and its rendered value. Rendered, not JSON-encoded: `true`, `false`, `RuntimeDefault`, `10m`, `[ALL]`.
- The lines are sorted **bytewise ascending as whole lines**, after rendering. Not by path, not in template order, not in map order.
- Every line, including the last, is terminated by a single `\n`.
- The hash is the **first 12 characters of the lowercase hex encoding** of SHA-256 over the UTF-8 bytes of that string.

**The patch is a strategic merge patch with the container lists keyed by `name`.** It carries only the containers that have gaps and only the fields being filled: it never restates the container array, never reorders it and never mentions a container with no gaps (FR-02). The provenance annotation goes in the **same document**, so a target is never patched without its record (BR-08, NFR-02).

**Files:**

- Create: `pkg/plan/patch.go`
- Test: `pkg/plan/patch_test.go`

**Interfaces:**

- Consumes: `Target`, `Change` (Task 3); `v1alpha1.FilledAnnotation` (Task 1).
- Produces:
  - `func plan.Lines(changes []Change) []string`
  - `func plan.Canonical(t Target, changes []Change) string`
  - `func plan.Hash(t Target, changes []Change) string`
  - `func plan.Provenance(changes []Change) string`
  - `func plan.Patch(changes []Change) ([]byte, error)`

- [ ] **Step 1: Write the failing canonical-form and hash test**

Create `pkg/plan/patch_test.go`:

```go
package plan

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

var target = Target{Namespace: "tenant-a", Kind: "Deployment", Name: "api"}

// unsorted returns three changes in an order no sort would produce, so a
// canonical form that trusts its input fails here.
func unsorted() []Change {
	return []Change{
		{Path: "spec.template.spec.securityContext.seccompProfile.type", Value: "RuntimeDefault", JSON: "RuntimeDefault"},
		{Path: "spec.template.spec.containers[app].resources.requests.cpu", Value: "10m", JSON: "10m"},
		{Path: "spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation", Value: "false", JSON: false},
	}
}

// BR-07's canonical form, stated exactly: the identity line, then one sorted
// path=value line per change, each terminated by a newline.
func TestCanonicalForm(t *testing.T) {
	want := "Deployment/tenant-a/api\n" +
		"spec.template.spec.containers[app].resources.requests.cpu=10m\n" +
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation=false\n" +
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault\n"

	if got := Canonical(target, unsorted()); got != want {
		t.Errorf("Canonical =\n%q\nwant\n%q", got, want)
	}
}

// The identity is inside the hash, so two targets needing exactly the same
// fields do not share one — otherwise approving one would approve the other,
// and approving a subset deliberately, the normal way to use a gate like this,
// would be impossible (BR-07).
func TestHashIncludesTheTarget(t *testing.T) {
	changes := unsorted()
	a := Hash(Target{Namespace: "tenant-a", Kind: "Deployment", Name: "api"}, changes)
	b := Hash(Target{Namespace: "tenant-a", Kind: "Deployment", Name: "web"}, changes)
	c := Hash(Target{Namespace: "tenant-b", Kind: "Deployment", Name: "api"}, changes)
	d := Hash(Target{Namespace: "tenant-a", Kind: "StatefulSet", Name: "api"}, changes)

	for _, pair := range []struct {
		name string
		x, y string
	}{
		{"different name", a, b},
		{"different namespace", a, c},
		{"different kind", a, d},
	} {
		if pair.x == pair.y {
			t.Errorf("%s: hashes collide (%s)", pair.name, pair.x)
		}
	}

	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(a) {
		t.Errorf("hash = %q, want twelve lowercase hex digits", a)
	}
}

// A change to any value, not just to the set of paths, moves the hash: the
// guarantee is that the set of fields to be written and their values is
// unchanged since the operator saw it (BR-07).
func TestHashMovesWithAValue(t *testing.T) {
	before := []Change{{Path: "spec.template.spec.containers[app].resources.requests.cpu", Value: "10m", JSON: "10m"}}
	after := []Change{{Path: "spec.template.spec.containers[app].resources.requests.cpu", Value: "20m", JSON: "20m"}}

	if Hash(target, before) == Hash(target, after) {
		t.Error("the hash did not move when the value written changed")
	}
	if Hash(target, before) == Hash(target, nil) {
		t.Error("an empty change set hashes the same as a non-empty one")
	}
}

// The provenance annotation is the canonical lines without the identity: the
// leaf path of every field written and the value written to it (BR-08).
func TestProvenanceIsTheLeafPaths(t *testing.T) {
	want := "spec.template.spec.containers[app].resources.requests.cpu=10m\n" +
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation=false\n" +
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault"

	if got := Provenance(unsorted()); got != want {
		t.Errorf("Provenance =\n%q\nwant\n%q", got, want)
	}

	// Leaf paths, not the shallowest path created. Recording "resources"
	// rather than "resources.requests.cpu" would make an undo delete a limits
	// block a human added afterwards.
	for _, line := range strings.Split(Provenance(unsorted()), "\n") {
		if strings.HasSuffix(line, "resources=") || strings.HasSuffix(line, "securityContext=") {
			t.Errorf("a non-leaf path reached the annotation: %q", line)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/plan/ -run 'TestCanonical|TestHash|TestProvenance' -v`
Expected: FAIL — build error, `undefined: Canonical`, `undefined: Hash`, `undefined: Provenance`.

- [ ] **Step 3: Write the implementation**

Create `pkg/plan/patch.go`:

```go
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// hashLength is the number of hex digits published and approved. Twelve is
// short enough to copy by hand and long enough that a collision inside one
// plan is not a practical concern (BR-07).
const hashLength = 12

// Lines renders the changes as sorted "path=value" strings. It is the one
// rendering behind the change hash, the provenance annotation and the plan in
// status, so the three cannot disagree about what would be written.
//
// The sort is the load-bearing part. Everything upstream of here is a map —
// spec.resources.requests, securityContext, the container lists — and Go
// randomises map iteration. An unsorted serialisation produces a different
// hash on most passes, so every approval the operator copies is Stale before
// the write lands, and nothing in the status says why.
func Lines(changes []Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.Path+"="+c.Value)
	}
	slices.Sort(out)
	return out
}

// Canonical is the exact serialisation hashed for one target's change (BR-07):
// the target's identity on the first line, then one sorted "path=value" line
// per change, every line terminated by a single newline.
//
//	Deployment/tenant-a/api
//	spec.template.spec.containers[app].resources.requests.cpu=10m
//	spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault
//
// The identity line is what keeps two workloads needing identical fields from
// sharing a hash, so an approval can never travel from one target to another
// and an operator can approve a subset deliberately.
func Canonical(t Target, changes []Change) string {
	var b strings.Builder
	b.WriteString(t.String())
	b.WriteByte('\n')
	for _, line := range Lines(changes) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// Hash is the first 12 hex digits of SHA-256 over Canonical.
//
// Per target, never per plan. A single plan-wide hash cannot converge in a
// live environment: any CI deploy touching any workload in any of up to
// sixteen namespaces moves it, so the operator re-copies the hash and is stale
// again before the write lands (BR-07).
//
// The guarantee is exact and narrower than it may read: a target is patched
// only if the set of fields to be written, and their values, is unchanged
// since the operator saw it. A target edited in a way that does not change its
// gaps — a new image, a different command — has the same hash and is still
// patched.
func Hash(t Target, changes []Change) string {
	sum := sha256.Sum256([]byte(Canonical(t, changes)))
	return hex.EncodeToString(sum[:])[:hashLength]
}

// Provenance is the value of the hardening.acme.corp/filled annotation: the
// leaf path of every field written and the value written to it, one per line.
// It is Canonical without the identity line, which is already on the object
// carrying it.
//
// Leaf paths, not the shallowest path created. Recording "resources" rather
// than "resources.requests.cpu" would make an undo delete a limits block a
// human added afterwards, and that safety property is worth more than avoiding
// a cosmetic empty object where a field is removed (BR-08).
func Provenance(changes []Change) string {
	return strings.Join(Lines(changes), "\n")
}

// Patch renders the strategic merge patch for changes, with the container
// lists keyed by name, and the provenance annotation in the same document — so
// a target is never patched without its record (BR-08, NFR-02).
//
// The body carries only the containers that have gaps and only the fields
// being filled: it never restates the container array, never reorders it and
// never mentions a container with no gaps (FR-02).
//
// A target with no gaps yields no patch and no API call, so an empty change
// set is an error rather than an empty body: issuing it would restart every
// pod of the workload to write nothing.
func Patch(changes []Change) ([]byte, error) {
	if len(changes) == 0 {
		return nil, errors.New("no changes: a target with no gaps yields no patch and no API call")
	}

	root := map[string]any{}
	for _, c := range changes {
		if err := insert(root, c.Path, c.JSON); err != nil {
			return nil, err
		}
	}

	// The annotation key carries dots and a slash, so it is set directly
	// rather than through insert's path walker.
	metadata := child(root, "metadata")
	child(metadata, "annotations")[v1alpha1.FilledAnnotation] = Provenance(changes)

	// encoding/json emits object keys sorted, and the changes are sorted
	// before they arrive, so the same plan always marshals to the same bytes.
	return json.Marshal(root)
}

// child returns m[key] as a map, creating it if it is missing or not a map.
func child(m map[string]any, key string) map[string]any {
	next, ok := m[key].(map[string]any)
	if !ok {
		next = map[string]any{}
		m[key] = next
	}
	return next
}

// insert writes value at path inside root, creating the objects on the way.
//
// A segment spelled name[key] is a list whose merge key is "name": the entry
// is found or created by that key, which is what makes the result a strategic
// merge patch of one container rather than a replacement of the whole array.
func insert(root map[string]any, path string, value any) error {
	segments := strings.Split(path, ".")
	current := root
	for i, segment := range segments {
		last := i == len(segments)-1
		field, key, isList := cutList(segment)

		if isList {
			if last {
				return fmt.Errorf("path %q ends in a list segment; a change must name a leaf field", path)
			}
			if key == "" {
				return fmt.Errorf("path %q names a list entry with an empty merge key", path)
			}
			list, _ := current[field].([]any)
			var entry map[string]any
			for _, raw := range list {
				if m, ok := raw.(map[string]any); ok && m["name"] == key {
					entry = m
					break
				}
			}
			if entry == nil {
				entry = map[string]any{"name": key}
				current[field] = append(list, entry)
			}
			current = entry
			continue
		}

		if last {
			current[field] = value
			return nil
		}
		current = child(current, field)
	}
	return nil
}

// cutList splits "containers[app]" into "containers", "app", true, and any
// other segment into itself, "", false.
func cutList(segment string) (field, key string, ok bool) {
	open := strings.IndexByte(segment, '[')
	if open < 0 || !strings.HasSuffix(segment, "]") {
		return segment, "", false
	}
	return segment[:open], segment[open+1 : len(segment)-1], true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/plan/ -run 'TestCanonical|TestHash|TestProvenance' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/plan/patch.go pkg/plan/patch_test.go
git commit -m "feat(plan): canonical serialisation, per-target change hash and provenance

The canonical form is the target identity, then one sorted path=value line
per change. Sorted because everything upstream is a map and Go randomises
iteration; the identity is inside the hash so an approval cannot travel from
one target to another."
```

- [ ] **Step 6: Write the hash-stability test (Review Focus 1)**

This is the one test that, if it ever fails, means every approval in production is worthless. Append to `pkg/plan/patch_test.go`:

```go
// Review Focus 1: the change hash must not move between two passes over an
// unchanged target. Everything feeding it is a map — the requests, the
// securityContext, the container lists — and Go randomises map iteration. A
// hash that moves makes every approval the operator copies Stale before the
// write lands, and nothing in the status says why.
//
// The loop count is deliberate: one comparison would pass by chance on a map
// small enough to iterate in insertion order.
func TestHashIsStableAcrossPasses(t *testing.T) {
	pod := func() *corev1.PodSpec {
		return &corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "setup"}},
			Containers:     []corev1.Container{{Name: "app"}, {Name: "sidecar"}, {Name: "proxy"}},
		}
	}

	// Two policies built from separately constructed maps, so the map
	// literals have independent internal layouts.
	first := Policy{Requests: requests("10m", "32Mi"), ReadOnlyRootFilesystem: true}
	second := Policy{Requests: requests("10m", "32Mi"), ReadOnlyRootFilesystem: true}

	want := Hash(target, Build(pod(), first).Changes)
	wantCanonical := Canonical(target, Build(pod(), first).Changes)
	wantBody, err := Patch(Build(pod(), first).Changes)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	for i := range 200 {
		p := Build(pod(), second)

		if got := Hash(target, p.Changes); got != want {
			t.Fatalf("pass %d: hash = %s, want %s — the serialisation depends on map iteration order", i, got, want)
		}
		if got := Canonical(target, p.Changes); got != wantCanonical {
			t.Fatalf("pass %d: canonical form moved:\n%q\nwant\n%q", i, got, wantCanonical)
		}
		body, err := Patch(p.Changes)
		if err != nil {
			t.Fatalf("pass %d: Patch: %v", i, err)
		}
		if !bytes.Equal(body, wantBody) {
			t.Fatalf("pass %d: patch body moved:\n%s\nwant\n%s", i, body, wantBody)
		}
	}
}

// Sorting is over whole rendered lines, not over paths only, so two changes
// that share a path prefix order the same way every time.
func TestLinesAreSortedWhole(t *testing.T) {
	changes := []Change{
		{Path: "b", Value: "1"},
		{Path: "a", Value: "2"},
		{Path: "a", Value: "1"},
	}
	got := Lines(changes)
	want := []string{"a=1", "a=2", "b=1"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Lines = %v, want %v", got, want)
			break
		}
	}
}
```

- [ ] **Step 7: Run it**

Run: `go test ./pkg/plan/ -run 'TestHashIsStable|TestLinesAreSorted' -count=5 -v`
Expected: PASS. `-count=5` is not decoration — it re-seeds map iteration, which is the whole point of the test.

- [ ] **Step 8: Write the patch-body test (AC-01, AC-04, AC-11)**

Append to `pkg/plan/patch_test.go`:

```go
// The patch body: a strategic merge patch keyed by container name, carrying
// only the containers with gaps and only the fields being filled, with the
// provenance annotation in the same document (FR-02, BR-08, AC-11).
func TestPatchBody(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup"}},
		Containers: []corev1.Container{
			{Name: "app"},
			// Already hardened and already requesting: it must not appear.
			{
				Name: "sidecar",
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{Requests: requests("1m", "8Mi")},
			},
		},
	}

	body, err := Patch(Build(pod, basic()).Changes)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("the patch is not valid JSON: %v\n%s", err, body)
	}

	// No limits key anywhere in the document (AC-04).
	if bytes.Contains(body, []byte(`"limits"`)) {
		t.Errorf("the patch contains a limits key:\n%s", body)
	}

	// The provenance annotation rides in the same request (BR-08, NFR-02).
	metadata, _ := got["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	filled, ok := annotations[v1alpha1.FilledAnnotation].(string)
	if !ok {
		t.Fatalf("no %s annotation in the patch:\n%s", v1alpha1.FilledAnnotation, body)
	}
	if filled != Provenance(Build(pod, basic()).Changes) {
		t.Errorf("annotation = %q, want the provenance lines", filled)
	}
	for _, want := range []string{
		"spec.template.spec.securityContext.runAsNonRoot=true",
		"spec.template.spec.containers[app].resources.requests.cpu=10m",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop=[ALL]",
	} {
		if !strings.Contains(filled, want) {
			t.Errorf("annotation missing %q:\n%s", want, filled)
		}
	}

	spec, _ := got["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)

	// Pod level: the values are typed, not stringified.
	sc, _ := podSpec["securityContext"].(map[string]any)
	if sc["runAsNonRoot"] != true {
		t.Errorf("runAsNonRoot = %#v, want the bool true", sc["runAsNonRoot"])
	}
	seccomp, _ := sc["seccompProfile"].(map[string]any)
	if seccomp["type"] != "RuntimeDefault" {
		t.Errorf("seccompProfile.type = %#v", seccomp["type"])
	}

	// Container level: keyed by name, one entry, the hardened sidecar absent.
	list, _ := podSpec["containers"].([]any)
	if len(list) != 1 {
		t.Fatalf("containers = %v, want only the one with gaps", list)
	}
	entry, _ := list[0].(map[string]any)
	if entry["name"] != "app" {
		t.Errorf("containers[0].name = %v, want app", entry["name"])
	}
	if _, restated := entry["image"]; restated {
		t.Error("the patch restates a field it is not filling")
	}
	csc, _ := entry["securityContext"].(map[string]any)
	if csc["allowPrivilegeEscalation"] != false {
		t.Errorf("allowPrivilegeEscalation = %#v, want the bool false", csc["allowPrivilegeEscalation"])
	}
	caps, _ := csc["capabilities"].(map[string]any)
	drop, _ := caps["drop"].([]any)
	if len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("capabilities.drop = %#v, want [ALL]", caps["drop"])
	}
	requestsOut, _ := entry["resources"].(map[string]any)["requests"].(map[string]any)
	if requestsOut["cpu"] != "10m" || requestsOut["memory"] != "32Mi" {
		t.Errorf("requests = %#v", requestsOut)
	}

	// initContainers is a separate list and is patched the same way (BR-02).
	initList, _ := podSpec["initContainers"].([]any)
	if len(initList) != 1 {
		t.Fatalf("initContainers = %v, want one entry", initList)
	}
	if initList[0].(map[string]any)["name"] != "setup" {
		t.Errorf("initContainers[0].name = %v, want setup", initList[0].(map[string]any)["name"])
	}
}

// A target with no gaps yields no patch and no API call, so an empty change
// set must be refused rather than turned into an empty body that would restart
// every pod to write nothing (FR-02).
func TestPatchRefusesAnEmptyChangeSet(t *testing.T) {
	if _, err := Patch(nil); err == nil {
		t.Error("Patch(nil) returned a body; a target with no gaps issues no API call")
	}
}

// An init container and a regular container of the same name cannot occur —
// Kubernetes requires names to be unique across the two lists (BR-02) — but
// the path format keeps them distinct regardless, so a bug upstream cannot
// merge one into the other.
func TestListSegmentsAreDistinctPerList(t *testing.T) {
	body, err := Patch([]Change{
		{Path: "spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation", Value: "false", JSON: false},
		{Path: "spec.template.spec.initContainers[app].securityContext.allowPrivilegeEscalation", Value: "false", JSON: false},
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	podSpec := got["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if len(podSpec["containers"].([]any)) != 1 || len(podSpec["initContainers"].([]any)) != 1 {
		t.Errorf("the two lists were merged:\n%s", body)
	}
}

// A malformed path is a programming error, and it must surface as one rather
// than as a patch body that quietly writes somewhere else.
func TestPatchRejectsAMalformedPath(t *testing.T) {
	for _, path := range []string{
		"spec.template.spec.containers[app]",
		"spec.template.spec.containers[]",
	} {
		if _, err := Patch([]Change{{Path: path, Value: "x", JSON: "x"}}); err == nil {
			t.Errorf("Patch accepted the malformed path %q", path)
		}
	}
}
```

- [ ] **Step 9: Run the whole package and check coverage**

Run: `go test ./pkg/plan/ -race -cover -v`
Expected: PASS, coverage ≥90%.

If `TestPatchBody` fails with `containers = [...], want only the one with gaps`, `Build` is emitting changes for a container that has none — check the `hasRequest` and `AllowPrivilegeEscalation` branches in Tasks 3 and 4, not `insert`.

- [ ] **Step 10: Commit**

```bash
git add pkg/plan
git commit -m "test(plan): patch body, provenance and hash stability

Covers AC-01's shape, AC-04's no-limits rule and AC-11's same-request
annotation, plus the hash-stability case no acceptance criterion names:
200 passes over separately built policies must produce byte-identical
canonical forms, hashes and patch bodies."
```

---

### Task 6: Target discovery and exclusions (BR-04, BR-05, AC-07, AC-12)

Everything that decides what is a target, what is excluded, what is refused and what is merely reported. This is the first task that touches a client.

Three distinctions the spec draws and this task must keep apart:

- **Excluded** — never read as a target at all: protected namespaces, anything carrying a controlling `ownerReference`, anything annotated `hardening.acme.corp/skip: "true"`.
- **Refused** — a target kind, but one where the patch would not roll out: `spec.paused`, an `OnDelete` update strategy, a StatefulSet `rollingUpdate.partition` above zero. The patch would sit inert and the workload would break at some arbitrary later moment — a node drain, an eviction, a partition lowered days afterwards. That is the late, silent failure class BR-02 uses to justify making `readOnlyRootFilesystem` opt-in, so consistency requires refusing it rather than writing it.
- **Reported** — not a target kind at all: bare Pods, Jobs, standalone ReplicaSets, and the ReplicaSets owned by a Deployment.

**CronJobs are reported in their own right, which costs one RBAC line.** D-08 requires them reported; NFR-03 enumerates `pods`, `jobs`, `replicasets`, `limitranges` and `namespaces` without naming `cronjobs`. Reporting them through the Jobs they own would stay inside that enumeration, but a CronJob between schedules owns no Job and would vanish from the report — and BR-04 is explicit that findings are enumerated whether or not they can be acted on, because "a namespace reported as hardened while a root pod runs in it is a lie". NFR-03 reads as the access the feature needs rather than a closed list: the things it actually forbids are `resourcequotas`, `delete` on any workload, pod exec and secrets. So the ClusterRole adds `cronjobs` get/list, and a Job owned by a CronJob still names that owner so the two reports tie together.

**Files:**

- Create: `pkg/controller/targets.go`
- Test: `pkg/controller/targets_test.go`

**Interfaces:**

- Consumes: `plan.Target` (Task 3); `v1alpha1.SkipAnnotation`, `v1alpha1.Finding` (Task 1).
- Produces:
  - `type controller.HardeningReconciler struct { Kube kubernetes.Interface; Dyn dynamic.Interface; Protected map[string]bool; Timeout time.Duration; Now func() time.Time; mu sync.Mutex; verified map[string]string }` — declared here, used by Tasks 7, 8 and 9; do not redeclare it.
  - `type hardeningTarget struct { Ref plan.Target; Pod *corev1.PodSpec; Pods int; Rollout string; patch func(context.Context, []byte, metav1.PatchOptions) error }`
  - `func (r *HardeningReconciler) discover(ctx context.Context, namespace string) ([]hardeningTarget, []v1alpha1.Finding, error)`
  - `func excluded(obj metav1.Object) string`
  - `func controllerOf(obj metav1.Object) *metav1.OwnerReference`

  The `rejection` type and `reject()` already exist in `pkg/controller/validate.go` (001). Reuse them; do not declare a second error type.

- [ ] **Step 1: Write the failing discovery test (AC-07, AC-12)**

Create `pkg/controller/targets_test.go`:

```go
package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// hardUID is this feature's request UID. It is distinct from 001's uid
// constant, which is already declared in validate_test.go in this package.
const hardUID = "1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708"

// newHardener builds a HardeningReconciler over a fake typed clientset seeded
// with objects. The dynamic client is filled in by the reconcile tests;
// discovery never touches it.
func newHardener(objects ...runtime.Object) *HardeningReconciler {
	return &HardeningReconciler{
		Kube: fake.NewClientset(objects...),
		Protected: map[string]bool{
			"kube-system": true, "kube-public": true, "kube-node-lease": true,
			"isolation-system": true,
		},
		Timeout: 5 * time.Second,
		Now:     func() time.Time { return time.Unix(1700000000, 0) },
	}
}

// template is a pod template with one container and nothing set, so every
// policy field is a gap.
func template(container string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: container, Image: "nginx"}}},
	}
}

func deployment(namespace, name string, mutate ...func(*appsv1.Deployment)) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.DeploymentSpec{Template: template("app")},
		Status:     appsv1.DeploymentStatus{Replicas: 3},
	}
	for _, m := range mutate {
		m(d)
	}
	return d
}

func statefulSet(namespace, name string, mutate ...func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.StatefulSetSpec{Template: template("app")},
		Status:     appsv1.StatefulSetStatus{Replicas: 1},
	}
	for _, m := range mutate {
		m(s)
	}
	return s
}

func daemonSet(namespace, name string, mutate ...func(*appsv1.DaemonSet)) *appsv1.DaemonSet {
	d := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.DaemonSetSpec{Template: template("agent")},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 3},
	}
	for _, m := range mutate {
		m(d)
	}
	return d
}

// controlledBy stamps a controlling ownerReference onto an object.
func controlledBy(kind, name string) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &yes}
}

// refs renders the discovered targets as "Kind/name" for comparison.
func refs(targets []hardeningTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Ref.Kind+"/"+t.Ref.Name)
	}
	return out
}

// findingReason returns the reason reported against one object, or "".
func findingReason(findings []v1alpha1.Finding, kind, name string) string {
	for _, f := range findings {
		if f.Kind == kind && f.Name == name {
			return f.Reason
		}
	}
	return ""
}

// All three kinds carry the template at the same path, so all three are one
// code path (BR-04). The order is deterministic — kind, then name — so a retry
// resumes predictably and the log reads in the same order as the preview
// (FR-04).
func TestDiscoverAllThreeKinds(t *testing.T) {
	r := newHardener(
		deployment("tenant-a", "web"),
		deployment("tenant-a", "api"),
		statefulSet("tenant-a", "db"),
		daemonSet("tenant-a", "agent"),
		deployment("tenant-b", "elsewhere"),
	)

	targets, _, err := r.discover(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	want := []string{"DaemonSet/agent", "Deployment/api", "Deployment/web", "StatefulSet/db"}
	got := refs(targets)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("targets = %v, want %v", got, want)
	}

	for _, target := range targets {
		if target.Pod == nil || len(target.Pod.Containers) == 0 {
			t.Errorf("%s: template not carried", target.Ref)
		}
		if target.Rollout == "" {
			t.Errorf("%s: no rollout mechanism reported (BR-09)", target.Ref)
		}
		if target.Ref.Namespace != "tenant-a" {
			t.Errorf("%s: wrong namespace", target.Ref)
		}
	}

	// Pods affected, per BR-09: replicas for Deployment and StatefulSet, the
	// desired node count for a DaemonSet.
	for _, c := range []struct {
		ref  string
		pods int
	}{{"DaemonSet/agent", 3}, {"Deployment/api", 3}, {"StatefulSet/db", 1}} {
		for _, target := range targets {
			if target.Ref.Kind+"/"+target.Ref.Name == c.ref && target.Pods != c.pods {
				t.Errorf("%s: pods = %d, want %d", c.ref, target.Pods, c.pods)
			}
		}
	}
}

// AC-07: a paused target, an OnDelete target and a StatefulSet with
// partition > 0 are each refused with a distinct reason and never patched.
// None of them rolls the patch out to every pod, so it would sit inert and the
// workload would break at some arbitrary later moment.
func TestDiscoverRefusesConfigurationsThatDoNotRollOut(t *testing.T) {
	partition := int32(2)
	r := newHardener(
		deployment("tenant-a", "paused", func(d *appsv1.Deployment) { d.Spec.Paused = true }),
		statefulSet("tenant-a", "on-delete", func(s *appsv1.StatefulSet) {
			s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
		}),
		statefulSet("tenant-a", "partitioned", func(s *appsv1.StatefulSet) {
			s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
			}
		}),
		daemonSet("tenant-a", "ds-on-delete", func(d *appsv1.DaemonSet) {
			d.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}
		}),
		deployment("tenant-a", "fine"),
	)

	targets, findings, err := r.discover(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got := refs(targets); len(got) != 1 || got[0] != "Deployment/fine" {
		t.Fatalf("targets = %v, want only Deployment/fine", got)
	}

	for _, c := range []struct{ kind, name, wants string }{
		{"Deployment", "paused", "paused"},
		{"StatefulSet", "on-delete", "OnDelete"},
		{"StatefulSet", "partitioned", "partition"},
		{"DaemonSet", "ds-on-delete", "OnDelete"},
	} {
		reason := findingReason(findings, c.kind, c.name)
		if reason == "" {
			t.Errorf("%s/%s: not reported", c.kind, c.name)
			continue
		}
		if !strings.Contains(reason, c.wants) {
			t.Errorf("%s/%s: reason = %q, want it to mention %q", c.kind, c.name, reason, c.wants)
		}
	}

	// The reasons must be distinct, not one generic "refused".
	seen := map[string]bool{}
	for _, f := range findings {
		if seen[f.Reason] {
			t.Errorf("two targets share the reason %q; AC-07 requires a distinct one each", f.Reason)
		}
		seen[f.Reason] = true
	}
}

// AC-12: a skip-annotated target, a ReplicaSet owned by a Deployment, a bare
// Pod and a Job are each reported with a distinct reason and none is a target.
//
// Findings are enumerated whether or not they can be acted on: a namespace
// reported as hardened while a root pod runs in it is a lie, and silence is
// how that lie gets told (BR-04).
func TestDiscoverExcludesAndReports(t *testing.T) {
	r := newHardener(
		deployment("tenant-a", "skipped", func(d *appsv1.Deployment) {
			d.Annotations = map[string]string{v1alpha1.SkipAnnotation: "true"}
		}),
		deployment("tenant-a", "operator-owned", func(d *appsv1.Deployment) {
			d.OwnerReferences = []metav1.OwnerReference{controlledBy("Widget", "my-widget")}
		}),
		deployment("tenant-a", "fine"),
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "fine-abc123", Namespace: "tenant-a",
			OwnerReferences: []metav1.OwnerReference{controlledBy("Deployment", "fine")},
		}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "standalone", Namespace: "tenant-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "fine-abc123-xyz", Namespace: "tenant-a",
			OwnerReferences: []metav1.OwnerReference{controlledBy("ReplicaSet", "fine-abc123")},
		}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "tenant-a"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "tenant-a"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-28899", Namespace: "tenant-a",
			OwnerReferences: []metav1.OwnerReference{controlledBy("CronJob", "nightly")},
		}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "tenant-a"}},
		// Between schedules: owns no Job, and must still be reported.
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "weekly", Namespace: "tenant-a"}},
	)

	targets, findings, err := r.discover(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got := refs(targets); len(got) != 1 || got[0] != "Deployment/fine" {
		t.Fatalf("targets = %v, want only Deployment/fine", got)
	}

	for _, c := range []struct{ kind, name, wants string }{
		{"Deployment", "skipped", v1alpha1.SkipAnnotation},
		{"Deployment", "operator-owned", "Widget/my-widget"},
		{"ReplicaSet", "fine-abc123", "Deployment/fine"},
		{"ReplicaSet", "standalone", "standalone"},
		{"Pod", "bare", "immutable"},
		{"Job", "migrate", "immutable"},
		{"Job", "nightly-28899", "CronJob/nightly"},
		// Reported in their own right. "weekly" owns no Job at all, so a
		// report routed through Jobs would have missed it entirely (D-08).
		{"CronJob", "nightly", "next schedule"},
		{"CronJob", "weekly", "next schedule"},
	} {
		reason := findingReason(findings, c.kind, c.name)
		if reason == "" {
			t.Errorf("%s/%s: not reported", c.kind, c.name)
			continue
		}
		if !strings.Contains(reason, c.wants) {
			t.Errorf("%s/%s: reason = %q, want it to mention %q", c.kind, c.name, reason, c.wants)
		}
	}

	// A pod belonging to a workload is not a bare pod: that workload is the
	// target, and reporting every replica would drown the status.
	if reason := findingReason(findings, "Pod", "fine-abc123-xyz"); reason != "" {
		t.Errorf("an owned pod was reported: %q", reason)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/controller/ -run TestDiscover -v`
Expected: FAIL — build error, `undefined: HardeningReconciler`, `undefined: hardeningTarget`, `undefined: (*HardeningReconciler).discover`.

- [ ] **Step 3: Write the implementation**

Create `pkg/controller/targets.go`:

```go
package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"
)

// HardeningReconciler drives WorkloadHardening objects. It shares the binary,
// the clients and the workqueue with the NetworkIsolation reconciler but keeps
// its own state: the dry-run cache of FR-03.
//
// There is no finalizer field and no workload informer, deliberately. Isolation
// is desired state and must persist while pods come and go; hardening is not,
// and a field written into a workload's own template needs no custodian. A
// continuous reconcile would eventually overwrite a deliberate later change —
// someone raising a memory limit after an OOMKill — and start a rollout to do
// it (FR-05, D-02).
type HardeningReconciler struct {
	Kube      kubernetes.Interface
	Dyn       dynamic.Interface
	Protected map[string]bool
	Timeout   time.Duration
	Now       func() time.Time

	// mu guards verified, which one worker writes and which Run's resync
	// reads. The queue serialises a key, but the map is shared across keys.
	mu sync.Mutex
	// verified records, per request and target, the change hash whose dry-run
	// the API server last accepted. Consulted only while unarmed (FR-03).
	verified map[string]string
}

// Rollout mechanisms, per BR-02's table. Reported so that a single-replica
// StatefulSet is visibly a different proposition from a three-replica
// Deployment (BR-09). The tool does not stage, throttle or canary: the
// restarts are inherent to what was asked for, and the workload's own
// maxUnavailable and readiness gating are the mechanism that bounds them
// (D-10).
const (
	rolloutDeployment  = "RollingUpdate: maxSurge 25% rounds up, maxUnavailable 25% rounds down; new pods are created first, and at 3 replicas or fewer every old pod keeps serving"
	rolloutStatefulSet = "RollingUpdate: reverse ordinal, one pod at a time, terminate-then-create, no surge; at 1 replica the workload is down until reverted"
	rolloutDaemonSet   = "RollingUpdate: maxUnavailable 1, maxSurge 0, delete-then-create per node; the rollout halts after one node"
)

// Refusal reasons. Each is distinct, because "refused" without the cause tells
// an operator nothing about which knob to turn (AC-07).
const (
	refusedPaused   = "spec.paused is true: the patch would not roll out, so it would sit inert and the workload would break at a later drain, eviction or scale (BR-04)"
	refusedOnDelete = "the update strategy is OnDelete: the patch would not roll out, so it would sit inert and the workload would break at a later drain, eviction or scale (BR-04)"
)

// hardeningTarget is one workload this pass will consider.
type hardeningTarget struct {
	Ref plan.Target
	// Pod is the target's spec.template.spec. It is the only thing read and
	// the only thing patched; running pods are never patched (Terminology).
	Pod *corev1.PodSpec
	// Pods is how many pods a patch would restart (BR-09).
	Pods int
	// Rollout is the mechanism from BR-02's table that applies to this kind.
	Rollout string
	// patch issues one strategic merge patch against this target's own kind,
	// so nothing downstream needs a type switch.
	patch func(ctx context.Context, body []byte, opts metav1.PatchOptions) error
}

// discover enumerates one namespace: every target this tool may patch, in a
// deterministic order, and a finding for everything it may not.
func (r *HardeningReconciler) discover(ctx context.Context, namespace string) ([]hardeningTarget, []v1alpha1.Finding, error) {
	var (
		targets  []hardeningTarget
		findings []v1alpha1.Finding
	)
	report := func(kind, name, reason string) {
		findings = append(findings, v1alpha1.Finding{Namespace: namespace, Kind: kind, Name: name, Reason: reason})
	}

	apps := r.Kube.AppsV1()

	deployments, err := apps.Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if why := excluded(d); why != "" {
			report("Deployment", d.Name, why)
			continue
		}
		// A Deployment has no OnDelete strategy and no partition; paused is
		// the only configuration that stops the patch rolling out.
		if d.Spec.Paused {
			report("Deployment", d.Name, refusedPaused)
			continue
		}
		name := d.Name
		targets = append(targets, hardeningTarget{
			Ref:     plan.Target{Namespace: namespace, Kind: "Deployment", Name: name},
			Pod:     &d.Spec.Template.Spec,
			Pods:    int(d.Status.Replicas),
			Rollout: rolloutDeployment,
			patch: func(ctx context.Context, body []byte, opts metav1.PatchOptions) error {
				_, err := apps.Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, opts)
				return err
			},
		})
	}

	statefulSets, err := apps.StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range statefulSets.Items {
		s := &statefulSets.Items[i]
		if why := excluded(s); why != "" {
			report("StatefulSet", s.Name, why)
			continue
		}
		if s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
			report("StatefulSet", s.Name, refusedOnDelete)
			continue
		}
		if ru := s.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil && *ru.Partition > 0 {
			report("StatefulSet", s.Name, fmt.Sprintf(
				"rollingUpdate.partition is %d: the patch would not reach the ordinals below it, so it would sit inert until the partition is lowered days afterwards (BR-04)",
				*ru.Partition))
			continue
		}
		name := s.Name
		targets = append(targets, hardeningTarget{
			Ref:     plan.Target{Namespace: namespace, Kind: "StatefulSet", Name: name},
			Pod:     &s.Spec.Template.Spec,
			Pods:    int(s.Status.Replicas),
			Rollout: rolloutStatefulSet,
			patch: func(ctx context.Context, body []byte, opts metav1.PatchOptions) error {
				_, err := apps.StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, opts)
				return err
			},
		})
	}

	daemonSets, err := apps.DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range daemonSets.Items {
		d := &daemonSets.Items[i]
		if why := excluded(d); why != "" {
			report("DaemonSet", d.Name, why)
			continue
		}
		if d.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
			report("DaemonSet", d.Name, refusedOnDelete)
			continue
		}
		name := d.Name
		targets = append(targets, hardeningTarget{
			Ref:     plan.Target{Namespace: namespace, Kind: "DaemonSet", Name: name},
			Pod:     &d.Spec.Template.Spec,
			Pods:    int(d.Status.DesiredNumberScheduled),
			Rollout: rolloutDaemonSet,
			patch: func(ctx context.Context, body []byte, opts metav1.PatchOptions) error {
				_, err := apps.DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, opts)
				return err
			},
		})
	}

	replicaSets, err := apps.ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range replicaSets.Items {
		rs := &replicaSets.Items[i]
		if owner := controllerOf(rs); owner != nil {
			report("ReplicaSet", rs.Name, ownedBy(owner))
			continue
		}
		report("ReplicaSet", rs.Name, "a standalone ReplicaSet is out of scope: rare enough not to justify a fourth code path (D-08)")
	}

	jobs, err := r.Kube.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		reason := "spec.template is immutable after creation, so a Job is reported and never patched (BR-04, D-08)"
		if owner := controllerOf(j); owner != nil && owner.Kind == "CronJob" {
			// Named so the operator can tie this Job back to the schedule that
			// created it. The CronJob itself is reported separately below.
			reason = fmt.Sprintf(
				"created by CronJob/%s, whose template this tool does not patch (D-08)",
				owner.Name)
		}
		report("Job", j.Name, reason)
	}

	// Reported in their own right, not through the Jobs they own: a CronJob
	// between schedules owns no Job, and BR-04 requires findings to be
	// enumerated whether or not they can be acted on.
	crons, err := r.Kube.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range crons.Items {
		report("CronJob", crons.Items[i].Name,
			"a CronJob needs a second template path and has no rollout net at all — a broken one simply fails on its next schedule (D-08)")
	}

	pods, err := r.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if controllerOf(p) != nil {
			// It belongs to a workload, and that workload is the target.
			// Reporting every replica would drown the status in noise.
			continue
		}
		report("Pod", p.Name, "securityContext is immutable on an existing pod, and deleting someone's workload to improve it is not a trade this tool makes (BR-04, D-09)")
	}

	// Deterministic order — namespace, kind, name — so a retry resumes
	// predictably and the log reads in the same order as the preview (FR-04).
	slices.SortFunc(targets, func(a, b hardeningTarget) int {
		return strings.Compare(a.Ref.String(), b.Ref.String())
	})
	slices.SortFunc(findings, func(a, b v1alpha1.Finding) int {
		return strings.Compare(a.Kind+"/"+a.Name, b.Kind+"/"+b.Name)
	})
	return targets, findings, nil
}

// excluded reports why obj must never be read as a target, or "" when it may
// be (BR-04). The skip annotation is checked first so an operator who set it
// sees their own reason rather than an ownership one.
func excluded(obj metav1.Object) string {
	if obj.GetAnnotations()[v1alpha1.SkipAnnotation] == "true" {
		return "annotated " + v1alpha1.SkipAnnotation + `="true": the escape hatch for a workload that needs what the policy would take away (BR-04)`
	}
	if owner := controllerOf(obj); owner != nil {
		return ownedBy(owner)
	}
	return ""
}

// ownedBy is the reason for an object whose owner is the target instead.
func ownedBy(owner *metav1.OwnerReference) string {
	return fmt.Sprintf("controlled by %s/%s: the owner is the target instead, so patching this would be undone by its controller (BR-04)",
		owner.Kind, owner.Name)
}

// controllerOf returns the controlling ownerReference, or nil. Only a
// controlling reference counts: a non-controlling one records a relationship
// without implying anyone rewrites this object's template.
func controllerOf(obj metav1.Object) *metav1.OwnerReference {
	for _, o := range obj.GetOwnerReferences() {
		if o.Controller != nil && *o.Controller {
			// Go 1.22 onwards gives each iteration its own variable, so
			// taking this address is safe.
			return &o
		}
	}
	return nil
}
```

- [ ] **Step 4: Vendor the new imports and run the tests**

```bash
go mod vendor
go test ./pkg/controller/ -run TestDiscover -v
```

Expected: `vendor/k8s.io/api/batch/v1` and `vendor/k8s.io/client-go/kubernetes/typed/apps/v1` are already present, so `go mod vendor` reports no change; `go.mod` and `go.sum` are untouched. All three tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/controller vendor
git commit -m "feat(controller): target discovery, exclusions and refusals

Covers AC-07 (paused, OnDelete and partition each refused with a distinct
reason) and AC-12 (skip annotation, controlling ownerReference, bare Pods,
Jobs and ReplicaSets each reported). CronJobs are reported in their own
right: one between schedules owns no Job to be reported through, and BR-04
requires findings enumerated whether or not they can be acted on."
```

---

### Task 7: Preview — dry-run, the cache, and the rendered plan (FR-03, AC-08, AC-10)

The pass that computes a plan and writes nothing. Three things here are specific and easy to get subtly wrong.

**The fake clientset does not honour `DryRun`.** Verified against `k8s.io/client-go@v0.37.1`: a `Patch` carrying `DryRun: [All]` mutates the fake's object tracker exactly as a real patch would. So a test that asserts "stored objects are unchanged" against a bare fake passes vacuously — it proves nothing, and the feature's central safety property would be untested. Every test in Tasks 7 and 8 installs a reactor that makes the fake behave like an API server, and the reactor is also how "a preview writes nothing" becomes a real assertion.

**The cache is consulted only while unarmed.** Otherwise a `Previewed` object re-runs the full admission chain, every mutating and validating webhook included, for every target in up to sixteen namespaces on every resync, forever, on behalf of an object nobody armed. But the cache key is the target's own change, which does **not** move when a webhook is installed, a namespace gains a Pod Security label or a LimitRange appears — so a cached acceptance says nothing about whether a write would be accepted now. An apply therefore always dry-runs in the same pass, cache or not (FR-03, NFR-02).

**A target with no gaps yields no plan row at all.** Not a row with an empty field list: no patch body is built, no dry-run is issued, and nothing about it reaches status. That is Review Focus 4, and it is also what makes AC-13's convergence work.

**Files:**

- Create: `pkg/controller/hardening.go`, `pkg/controller/execute.go`
- Test: `pkg/controller/hardening_test.go`

**Interfaces:**

- Consumes: `HardeningReconciler`, `hardeningTarget`, `discover` (Task 6); `rejection`, `reject` (001's `validate.go`); `plan.Build`, `plan.Cover`, `plan.Hash`, `plan.Lines`, `plan.Patch`, `plan.Policy`, `plan.Target` (Tasks 3–5); `v1alpha1.*` (Task 1).
- Produces:
  - `func (r *HardeningReconciler) Reconcile(ctx context.Context, key string) error`
  - `func (r *HardeningReconciler) validate(ctx context.Context, w *v1alpha1.WorkloadHardening) (map[string]plan.Policy, error)`
  - `func (r *HardeningReconciler) evaluate(ctx context.Context, logger klog.Logger, w *v1alpha1.WorkloadHardening) error`
  - `func (r *HardeningReconciler) setHardeningStatus(ctx context.Context, w *v1alpha1.WorkloadHardening, want v1alpha1.HardeningStatus) error`
  - `func (r *HardeningReconciler) execute(ctx context.Context, logger klog.Logger, w *v1alpha1.WorkloadHardening, t hardeningTarget, changes []plan.Change, row v1alpha1.TargetStatus) v1alpha1.TargetStatus`
  - `func (r *HardeningReconciler) cached(w *v1alpha1.WorkloadHardening, ref plan.Target, hash string) bool`
  - `func (r *HardeningReconciler) remember(w *v1alpha1.WorkloadHardening, ref plan.Target, hash string)`
  - `func (r *HardeningReconciler) forget(w *v1alpha1.WorkloadHardening, ref plan.Target)`
  - `func dryRun() metav1.PatchOptions`

  Task 8 adds the armed branch of `execute`, plus `phaseFor`, `danglingApprovals`, `carry` and `approvedEarlier`, to `execute.go`.

- [ ] **Step 1: Write the failing preview test (AC-08)**

Create `pkg/controller/hardening_test.go`:

```go
package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// hardening builds an unarmed request over the given namespaces.
func hardening(namespaces ...string) *v1alpha1.WorkloadHardening {
	return &v1alpha1.WorkloadHardening{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-hardening", Namespace: "isolation-system", UID: hardUID},
		Spec: v1alpha1.HardeningSpec{
			Namespaces: namespaces,
			Resources: v1alpha1.ResourcePolicy{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			}},
		},
	}
}

// hardeningDynClient builds a fake dynamic client holding w. The custom list
// kind is required: the fake cannot infer one for an unregistered CRD.
func hardeningDynClient(t *testing.T, w *v1alpha1.WorkloadHardening) *dynamicfake.FakeDynamicClient {
	t.Helper()
	u, err := v1alpha1.HardeningToUnstructured(w)
	if err != nil {
		t.Fatalf("HardeningToUnstructured: %v", err)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			v1alpha1.Resource:          v1alpha1.Kind + "List",
			v1alpha1.HardeningResource: v1alpha1.HardeningKind + "List",
		},
		u,
	)
}

// storedHardening reads the request back out of the fake dynamic client.
func storedHardening(t *testing.T, r *HardeningReconciler, w *v1alpha1.WorkloadHardening) *v1alpha1.WorkloadHardening {
	t.Helper()
	u, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(w.Namespace).Get(context.Background(), w.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the request back: %v", err)
	}
	got, err := v1alpha1.HardeningFromUnstructured(u)
	if err != nil {
		t.Fatalf("HardeningFromUnstructured: %v", err)
	}
	return got
}

func hardeningKey(w *v1alpha1.WorkloadHardening) string { return w.Namespace + "/" + w.Name }

// dryRunGuard makes the fake behave like an API server: a patch carrying
// DryRun is answered from the stored object and never applied.
//
// Verified against client-go v0.37.1: the fake clientset does NOT honour
// DryRun — it mutates its object tracker exactly as a real patch would. So
// without this reactor, AC-08's "stored objects are unchanged" would pass
// only because the assertion is checking a value the fake already clobbered.
func dryRunGuard(t *testing.T, c *fake.Clientset) {
	t.Helper()
	c.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p, ok := a.(k8stesting.PatchActionImpl)
		if !ok || !slices.Contains(p.PatchOptions.DryRun, metav1.DryRunAll) {
			return false, nil, nil
		}
		obj, err := c.Tracker().Get(a.GetResource(), a.GetNamespace(), p.Name)
		return true, obj, err
	})
}

// patchActions returns the patch actions the fake recorded, in order.
func patchActions(t *testing.T, r *HardeningReconciler) []k8stesting.PatchActionImpl {
	t.Helper()
	var out []k8stesting.PatchActionImpl
	for _, a := range r.Kube.(*fake.Clientset).Actions() {
		if p, ok := a.(k8stesting.PatchActionImpl); ok {
			out = append(out, p)
		}
	}
	return out
}

// rowFor returns the plan row for one target, or the zero value.
func rowFor(w *v1alpha1.WorkloadHardening, kind, name string) v1alpha1.TargetStatus {
	for _, row := range w.Status.Plan {
		if row.Kind == kind && row.Name == name {
			return row
		}
	}
	return v1alpha1.TargetStatus{}
}

// storedTemplate reads a Deployment's template back out of the fake.
func storedTemplate(t *testing.T, r *HardeningReconciler, namespace, name string) *appsv1.Deployment {
	t.Helper()
	d, err := r.Kube.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading %s/%s: %v", namespace, name, err)
	}
	return d
}

// AC-08: preview writes nothing. Every changed target's patch is issued with
// DryRun, the stored objects are unchanged, and the per-target plan and hashes
// appear in status.
func TestPreviewWritesNothing(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), statefulSet("tenant-a", "db"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePreviewed {
		t.Fatalf("phase = %q (%s), want Previewed", got.Status.Phase, got.Status.Message)
	}
	if len(got.Status.Plan) != 2 {
		t.Fatalf("status.plan = %+v, want two rows", got.Status.Plan)
	}
	if got.Status.LastReconcileTime == "" {
		t.Error("lastReconcileTime not set")
	}

	for _, row := range got.Status.Plan {
		if row.Outcome != v1alpha1.OutcomePlanned {
			t.Errorf("%s/%s: outcome = %q, want Planned; nothing is written while unarmed",
				row.Kind, row.Name, row.Outcome)
		}
		if len(row.Hash) != 12 {
			t.Errorf("%s/%s: hash = %q, want twelve hex digits", row.Kind, row.Name, row.Hash)
		}
		if len(row.Fields) == 0 {
			t.Errorf("%s/%s: no fields published; the operator cannot see what would change", row.Kind, row.Name)
		}
		if row.Rollout == "" {
			t.Errorf("%s/%s: no rollout mechanism published (BR-09)", row.Kind, row.Name)
		}
	}

	// Every patch was a dry-run, and there were exactly as many as there are
	// changed targets.
	patches := patchActions(t, r)
	if len(patches) != 2 {
		t.Fatalf("issued %d patches, want one dry-run per changed target", len(patches))
	}
	for _, p := range patches {
		if !slices.Contains(p.PatchOptions.DryRun, metav1.DryRunAll) {
			t.Errorf("patch of %s carried no DryRun: a preview must write nothing", p.Name)
		}
	}

	// The stored objects are unchanged. The dryRunGuard is what makes this
	// assertion mean anything.
	d := storedTemplate(t, r, "tenant-a", "api")
	if d.Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Error("a provenance annotation was written during a preview")
	}
	if d.Spec.Template.Spec.SecurityContext != nil {
		t.Error("a securityContext was written during a preview")
	}
	if c := d.Spec.Template.Spec.Containers[0]; c.SecurityContext != nil || len(c.Resources.Requests) != 0 {
		t.Error("a container was patched during a preview")
	}
}

// FR-05: validation is all or nothing across namespaces. The operator named
// two and should get both or neither.
func TestValidationIsAllOrNothing(t *testing.T) {
	cases := []struct {
		name    string
		objects []runtime.Object
		request *v1alpha1.WorkloadHardening
		wants   string
	}{
		{
			name:    "a protected namespace",
			objects: []runtime.Object{ns("tenant-a"), ns("kube-system")},
			request: hardening("tenant-a", "kube-system"),
			wants:   "protected",
		},
		{
			name:    "the controller's own namespace",
			objects: []runtime.Object{ns("tenant-a"), ns("isolation-system")},
			request: hardening("tenant-a", "isolation-system"),
			wants:   "protected",
		},
		{
			name:    "a missing namespace",
			objects: []runtime.Object{ns("tenant-a"), deployment("tenant-a", "api")},
			request: hardening("tenant-a", "tenant-b"),
			wants:   `"tenant-b" does not exist`,
		},
		{
			name: "LimitRange bounds excluding the requested values",
			objects: []runtime.Object{
				ns("tenant-a"), deployment("tenant-a", "api"),
				&corev1.LimitRange{
					ObjectMeta: metav1.ObjectMeta{Name: "bounds", Namespace: "tenant-a"},
					Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
						Type: corev1.LimitTypeContainer,
						Min:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
					}}},
				},
			},
			request: hardening("tenant-a"),
			wants:   "min",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newHardener(c.objects...)
			r.Dyn = hardeningDynClient(t, c.request)
			dryRunGuard(t, r.Kube.(*fake.Clientset))

			if err := r.Reconcile(context.Background(), hardeningKey(c.request)); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			got := storedHardening(t, r, c.request)
			if got.Status.Phase != v1alpha1.PhaseRejected {
				t.Fatalf("phase = %q (%s), want Rejected", got.Status.Phase, got.Status.Message)
			}
			if !strings.Contains(got.Status.Message, c.wants) {
				t.Errorf("message = %q, want it to mention %q", got.Status.Message, c.wants)
			}
			if len(got.Status.Plan) != 0 {
				t.Errorf("status.plan = %+v, want nothing previewed", got.Status.Plan)
			}
			// Nothing was previewed, so no namespace was touched — not even
			// the valid one.
			if n := len(patchActions(t, r)); n != 0 {
				t.Errorf("issued %d patches, want none: the whole object is rejected", n)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/controller/ -run 'TestPreviewWritesNothing|TestValidationIsAllOrNothing' -v`
Expected: FAIL — build error, `undefined: (*HardeningReconciler).Reconcile`.

- [ ] **Step 3: Write the reconcile pass**

Create `pkg/controller/hardening.go`:

```go
package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"
)

// maxNamespaces mirrors the CRD. The schema pins it, but an object stored
// before the constraint tightened, or one that reached etcd another way, must
// not reach the discovery loop (BR-04).
const maxNamespaces = 16

// Reconcile drives one WorkloadHardening, named by its "namespace/name" key.
//
// Per pass: read the object; if the phase is Applied do nothing; validate
// namespaces and LimitRange bounds; enumerate targets; build the plan; then
// preview or apply per BR-07. There is no deletion branch — no finalizer means
// no object is ever stuck waiting on this controller, and deleting the request
// leaves the patches and the provenance annotations in place (FR-05).
func (r *HardeningReconciler) Reconcile(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	u, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Gone. With no finalizer there is nothing to undo: the patches and
		// the provenance annotations stay where they are (FR-05, BR-08).
		return nil
	}
	if err != nil {
		return err
	}

	logger := klog.FromContext(ctx).WithValues("request", string(u.GetUID()), "object", key)

	w, err := v1alpha1.HardeningFromUnstructured(u)
	if err != nil {
		// A stored object whose spec will not convert cannot be planned, and
		// retrying will not change that. Reporting it beats requeueing forever
		// behind a blank status, which is indistinguishable from an object the
		// controller has never seen.
		logger.Info("Rejected", "reason", err.Error())
		stub := &v1alpha1.WorkloadHardening{}
		stub.SetName(u.GetName())
		stub.SetNamespace(u.GetNamespace())
		stub.SetUID(u.GetUID())
		stub.SetResourceVersion(u.GetResourceVersion())
		// Written through the status subresource, which ignores everything
		// outside status — so the stub's empty spec never reaches etcd.
		return r.setHardeningStatus(ctx, stub, v1alpha1.HardeningStatus{
			Phase:   v1alpha1.PhaseRejected,
			Message: "spec cannot be read: " + err.Error(),
		})
	}

	// Applied is the only terminal phase, including for an object that reached
	// it with targets left Unapproved — but it is terminal only until the
	// approval changes. approvedPlan is the only mutable field in spec, so a
	// generation ahead of the one this status describes is exactly an
	// operator extending or correcting an approval: FR-01's next batch, or a
	// hash that matched nothing being fixed. A resync does not move
	// generation, so an untouched Applied object still issues no API calls
	// (AC-14, AC-18).
	//
	// Everything else is re-evaluated on every resync, so an object refused
	// for a missing namespace or out-of-range bounds recovers by itself once
	// the cause clears. A protected namespace is permanent and will be
	// re-evaluated pointlessly forever; that is accepted, because a second
	// terminality rule costs more than the wasted comparison (FR-05, FR-06).
	if w.Status.Phase == v1alpha1.PhaseApplied && w.Status.ObservedGeneration == w.Generation {
		return nil
	}
	return r.evaluate(ctx, logger, w)
}

// evaluate computes the plan and either previews it or applies it.
func (r *HardeningReconciler) evaluate(ctx context.Context, logger klog.Logger, w *v1alpha1.WorkloadHardening) error {
	policies, err := r.validate(ctx, w)
	var rej *rejection
	if errors.As(err, &rej) {
		logger.Info("Rejected", "reason", rej.reason)
		// Rejected is not terminal, and nothing was written, so there is
		// nothing to report per target.
		return r.setHardeningStatus(ctx, w, v1alpha1.HardeningStatus{
			Phase:   v1alpha1.PhaseRejected,
			Message: rej.reason,
		})
	}
	if err != nil {
		return r.reportFailure(ctx, logger, w, err)
	}

	var (
		rows     []v1alpha1.TargetStatus
		findings []v1alpha1.Finding
	)

	// Namespaces in sorted order, and targets within a namespace in
	// kind-then-name order, so a retry resumes predictably and the log reads
	// in the same order as the preview (FR-04).
	for _, namespace := range slices.Sorted(slices.Values(w.Spec.Namespaces)) {
		targets, namespaceFindings, err := r.discover(ctx, namespace)
		if err != nil {
			return r.reportFailure(ctx, logger, w, err)
		}
		findings = append(findings, namespaceFindings...)

		for _, target := range targets {
			p := plan.Build(target.Pod, policies[namespace])
			for _, f := range p.Findings {
				findings = append(findings, v1alpha1.Finding{
					Namespace: target.Ref.Namespace,
					Kind:      target.Ref.Kind,
					Name:      target.Ref.Name,
					Container: f.Container,
					Reason:    f.Reason,
				})
			}

			if len(p.Changes) == 0 {
				// A target with no gaps yields no patch and no API call — not
				// even a dry-run, and no row in the published plan (FR-02).
				// This is also what makes a retry converge: an already-patched
				// target has no gaps left, so it addresses only what failed.
				continue
			}

			rows = append(rows, r.execute(ctx, logger, w, target, p.Changes, v1alpha1.TargetStatus{
				Namespace: target.Ref.Namespace,
				Kind:      target.Ref.Kind,
				Name:      target.Ref.Name,
				Hash:      plan.Hash(target.Ref, p.Changes),
				Fields:    plan.Lines(p.Changes),
				Pods:      target.Pods,
				Rollout:   target.Rollout,
			}))
		}
	}

	rows = carry(w.Status.Plan, rows)
	phase, message := phaseFor(w, rows)
	logger.Info(string(phase), "message", message, "targets", len(rows), "findings", len(findings))

	if err := r.setHardeningStatus(ctx, w, v1alpha1.HardeningStatus{
		Phase:    phase,
		Message:  message,
		Plan:     rows,
		Findings: findings,
	}); err != nil {
		return err
	}
	return retryable(rows)
}

// reportFailure records a transient API failure and returns it so the queue
// retries. An object with a blank phase is indistinguishable from one the
// controller has never seen, so an operator cannot tell a wedged reconcile
// from a controller that is not running (NFR-04).
func (r *HardeningReconciler) reportFailure(ctx context.Context, logger klog.Logger, w *v1alpha1.WorkloadHardening, cause error) error {
	if statusErr := r.setHardeningStatus(ctx, w, v1alpha1.HardeningStatus{
		Phase:    v1alpha1.PhasePending,
		Message:  cause.Error(),
		Plan:     w.Status.Plan,
		Findings: w.Status.Findings,
	}); statusErr != nil {
		logger.Error(statusErr, "Could not report the failure")
	}
	return cause
}

// validate checks every precondition that can refuse the whole object before a
// single target is read.
//
// It is all or nothing across namespaces: if one named namespace is missing or
// refused, the object is rejected and no namespace is patched. The operator
// named two namespaces and should get both or neither. Execution, in contrast,
// is per target (FR-05, BR-07).
func (r *HardeningReconciler) validate(ctx context.Context, w *v1alpha1.WorkloadHardening) (map[string]plan.Policy, error) {
	if n := len(w.Spec.Namespaces); n == 0 || n > maxNamespaces {
		return nil, reject("spec.namespaces holds %d entries; between 1 and %d are required", n, maxNamespaces)
	}

	// Both are required by the schema, and both are re-read here because a
	// missing one would otherwise become a silent zero quantity written into
	// every container.
	requests := corev1.ResourceList{}
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		quantity, ok := w.Spec.Resources.Requests[name]
		if !ok {
			return nil, reject("spec.resources.requests.%s is required", name)
		}
		requests[name] = quantity
	}

	// BR-05, before any API call: naming a protected namespace rejects the
	// whole object, so nothing is previewed and nothing is written.
	for _, namespace := range w.Spec.Namespaces {
		if r.Protected[namespace] {
			return nil, reject("namespace %q is protected and may not be targeted", namespace)
		}
	}

	policies := map[string]plan.Policy{}
	for _, namespace := range slices.Sorted(slices.Values(w.Spec.Namespaces)) {
		if _, err := r.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, reject("namespace %q does not exist", namespace)
			}
			return nil, err
		}

		limitRanges, err := r.Kube.CoreV1().LimitRanges(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		coverage, why := plan.Cover(limitRanges.Items, requests)
		if why != "" {
			return nil, reject("namespace %q: %s", namespace, why)
		}

		policies[namespace] = plan.Policy{
			Requests:               requests,
			ReadOnlyRootFilesystem: w.Spec.SecurityContext.ReadOnlyRootFilesystem,
			Coverage:               coverage,
		}
	}
	return policies, nil
}

// setHardeningStatus writes status only when something other than the
// timestamp changed, so a resync of an unchanged object issues no writes at
// all (FR-06), exactly as in 001. lastReconcileTime therefore records when the
// observation last changed, not when the last pass ran.
func (r *HardeningReconciler) setHardeningStatus(ctx context.Context, w *v1alpha1.WorkloadHardening, want v1alpha1.HardeningStatus) error {
	// Stamped on every write: the status describes the spec that produced it,
	// and FR-05's terminality gate compares the two. Set before the equality
	// check so a pass that changes nothing but the generation still persists
	// it — otherwise an approval edit that turns out to be a no-op would leave
	// the object re-evaluating itself forever.
	want.ObservedGeneration = w.Generation
	want.LastReconcileTime = w.Status.LastReconcileTime
	if equality.Semantic.DeepEqual(w.Status, want) {
		return nil
	}
	want.LastReconcileTime = r.Now().UTC().Format(time.RFC3339)
	w.Status = want

	u, err := v1alpha1.HardeningToUnstructured(w)
	if err != nil {
		return err
	}
	out, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(w.Namespace).
		UpdateStatus(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	fresh, err := v1alpha1.HardeningFromUnstructured(out)
	if err != nil {
		return err
	}
	*w = *fresh
	return nil
}

// retryable returns an error when a pass left work that a retry could finish.
//
// Only a failure does. Stale is not retryable — recomputing produces the same
// hash and the same refusal until the operator updates the approval — and
// Unapproved is not a failure at all: approving a subset is the expected use
// of a per-target gate (FR-04, FR-06). Returning an error for either would
// spin the queue's backoff forever over a state only a human can clear.
func retryable(rows []v1alpha1.TargetStatus) error {
	var failed int
	for _, row := range rows {
		if row.Outcome == v1alpha1.OutcomeFailed {
			failed++
		}
	}
	if failed == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d targets failed", failed, len(rows))
}
```

- [ ] **Step 4: Write the per-target execution and the cache**

Create `pkg/controller/execute.go`:

```go
package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"
)

// dryRun is the option set that makes the API server complete every processing
// stage and then discard the result, so the preview reflects what the API
// server accepts rather than what the tool believes it would accept (FR-03).
//
// It does not cover the failures BR-02 is about. The API server accepts
// runAsNonRoot: true alongside runAsUser: 0 without complaint, and accepts a
// privileged root container under a pod-level runAsNonRoot: true; the pod then
// fails with CreateContainerConfigError. Every root-related failure is
// enforced by the kubelet or the kernel, not by API validation.
func dryRun() metav1.PatchOptions {
	return metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}}
}

// execute decides one target's fate and carries it out. Nothing outside this
// function writes to a workload.
func (r *HardeningReconciler) execute(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	changes []plan.Change,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	body, err := plan.Patch(changes)
	if err != nil {
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = err.Error()
		return row
	}

	if !w.Armed() {
		return r.preview(ctx, logger, w, t, body, row)
	}
	return r.apply(ctx, logger, w, t, body, row)
}

// preview dry-runs a changed target and publishes its hash. Nothing is written.
//
// While the object is unarmed, a target whose change hash is unchanged since
// the last pass is not re-sent: otherwise a Previewed object re-runs the full
// admission chain, every mutating and validating webhook included, for every
// target in up to sixteen namespaces on every resync, forever, on behalf of an
// object nobody armed (FR-03).
func (r *HardeningReconciler) preview(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	body []byte,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	row.Outcome = v1alpha1.OutcomePlanned
	if r.cached(w, t.Ref, row.Hash) {
		return row
	}
	if err := t.patch(ctx, body, dryRun()); err != nil {
		// The API server refuses a dry-run that would reach a webhook
		// declaring side effects other than None or NoneOnDryRun, and that
		// refusal is reported as this target's outcome rather than silently
		// swallowed (FR-03).
		logger.Info("Dry-run rejected", "target", t.Ref.String(), "reason", err.Error())
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = "dry-run rejected: " + err.Error()
		r.forget(w, t.Ref)
		return row
	}
	r.remember(w, t.Ref, row.Hash)
	return row
}

// cacheKey scopes a cached dry-run to one request object and one target, so
// two requests naming the same namespace never share an acceptance (G-03
// permits two such objects).
func cacheKey(w *v1alpha1.WorkloadHardening, ref plan.Target) string {
	return string(w.UID) + "|" + ref.String()
}

// cached reports whether this exact change was dry-run cleanly earlier.
func (r *HardeningReconciler) cached(w *v1alpha1.WorkloadHardening, ref plan.Target, hash string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verified[cacheKey(w, ref)] == hash
}

// remember records a clean dry-run.
func (r *HardeningReconciler) remember(w *v1alpha1.WorkloadHardening, ref plan.Target, hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verified == nil {
		r.verified = map[string]string{}
	}
	r.verified[cacheKey(w, ref)] = hash
}

// forget drops a cached acceptance, so a target whose dry-run failed is
// re-sent on the next pass rather than sitting behind a stale acceptance.
func (r *HardeningReconciler) forget(w *v1alpha1.WorkloadHardening, ref plan.Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.verified, cacheKey(w, ref))
}

// apply is written in Task 8. The stub keeps Task 7 compiling; it must be
// replaced, not kept.
func (r *HardeningReconciler) apply(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	body []byte,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	row.Outcome = v1alpha1.OutcomeUnapproved
	return row
}

// phaseFor and carry are written in Task 8. These stubs keep Task 7 compiling;
// they must be replaced, not kept.
func phaseFor(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) (v1alpha1.Phase, string) {
	return v1alpha1.PhasePreviewed, fmt.Sprintf("%d targets would change", len(rows))
}

func carry(previous, rows []v1alpha1.TargetStatus) []v1alpha1.TargetStatus { return rows }
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/controller/ -run 'TestPreviewWritesNothing|TestValidationIsAllOrNothing' -v`
Expected: PASS, including all four validation sub-tests.

If `TestPreviewWritesNothing` fails on `a container was patched during a preview`, the `dryRunGuard` reactor is not installed or is not matching — the fake applies dry-run patches for real, so the guard is the only thing standing between the test and a false pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/controller
git commit -m "feat(controller): preview dry-runs every changed target and writes nothing

Covers AC-08. Validation is all or nothing across namespaces; execution is
per target. The apply branch and the phase fold are stubbed; Task 8 writes
them."
```

- [ ] **Step 7: Write the dry-run cache test (AC-10, preview half)**

Append to `pkg/controller/hardening_test.go`:

```go
// AC-10, the unarmed half: an unchanged target is not re-dry-run on resync,
// and a changed one is. The alternative is re-running the full admission
// chain, every webhook included, for every target in up to sixteen namespaces
// on every resync, forever, on behalf of an object nobody armed (FR-03).
func TestUnarmedResyncSkipsUnchangedTargets(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if n := len(patchActions(t, r)); n != 2 {
		t.Fatalf("first pass issued %d dry-runs, want 2", n)
	}
	first := storedHardening(t, r, w)
	apiHash := rowFor(first, "Deployment", "api").Hash

	// Second pass, nothing changed: no dry-run at all.
	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("second pass issued %d dry-runs, want 0: both targets are unchanged", n)
	}
	// Status is unchanged too, so the pass issues no write of any kind.
	for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("unexpected write on an unchanged resync: %s %s", a.GetVerb(), a.GetSubresource())
		}
	}

	// Now move one target's change: give web an explicit request, so its gap
	// set shrinks and its hash moves. The other target is untouched.
	web := storedTemplate(t, r, "tenant-a", "web")
	web.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("50m"),
	}
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").Update(context.Background(), web, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("moving web's change: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("third pass: %v", err)
	}
	patches := patchActions(t, r)
	if len(patches) != 1 || patches[0].Name != "web" {
		t.Fatalf("third pass dry-ran %v, want only web", patches)
	}

	third := storedHardening(t, r, w)
	if got := rowFor(third, "Deployment", "api").Hash; got != apiHash {
		t.Errorf("api's hash moved from %s to %s although nothing about it changed", apiHash, got)
	}
	if rowFor(third, "Deployment", "web").Hash == rowFor(first, "Deployment", "web").Hash {
		t.Error("web's hash did not move although its gaps changed")
	}
}

// A dry-run refusal is this target's outcome, and the other targets are
// unaffected (FR-03, error table).
func TestDryRunRefusalIsPerTarget(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	r.Kube.(*fake.Clientset).PrependReactor("patch", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.PatchActionImpl).Name == "api" {
			return true, nil, apierrors.NewInternalError(
				errors.New(`admission webhook "mutate.example.com" does not declare sideEffects: None or NoneOnDryRun`))
		}
		return false, nil, nil
	})

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err == nil {
		t.Error("want an error so the key is requeued")
	}

	got := storedHardening(t, r, w)
	api := rowFor(got, "Deployment", "api")
	if api.Outcome != v1alpha1.OutcomeFailed {
		t.Errorf("api outcome = %q, want Failed", api.Outcome)
	}
	if !strings.Contains(api.Reason, "sideEffects") {
		t.Errorf("api reason = %q, want the API server's own message, not a swallowed one", api.Reason)
	}
	if web := rowFor(got, "Deployment", "web"); web.Outcome != v1alpha1.OutcomePlanned {
		t.Errorf("web outcome = %q, want Planned: other targets are unaffected", web.Outcome)
	}

	// A refused target is not cached, so the next pass tries again.
	r.Kube.(*fake.Clientset).ClearActions()
	_ = r.Reconcile(context.Background(), hardeningKey(w))
	var retried bool
	for _, p := range patchActions(t, r) {
		if p.Name == "api" {
			retried = true
		}
	}
	if !retried {
		t.Error("the refused target was not re-sent; a failed dry-run must not be cached as an acceptance")
	}
}
```

- [ ] **Step 8: Run it**

Run: `go test ./pkg/controller/ -run 'TestUnarmedResync|TestDryRunRefusal' -v`
Expected: PASS.

- [ ] **Step 9: Write the no-gaps test (Review Focus 4)**

Append to `pkg/controller/hardening_test.go`:

```go
// Review Focus 4: a target with no gaps yields no patch and no API call —
// stated in FR-02 but named by no acceptance criterion. It is also what makes
// AC-13's convergence work: an already-patched target has no gaps left, so a
// retry addresses only what failed.
func TestFullyHardenedTargetIssuesNoAPICall(t *testing.T) {
	hardened := deployment("tenant-a", "already-hardened", func(d *appsv1.Deployment) {
		d.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot:   ptrTo(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}
		d.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptrTo(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
		// Limits and no requests: the effective request equals the limit, so
		// there is no resource gap either, and the pod is Guaranteed (BR-01).
		d.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	})

	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), hardened)
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches for a target with no gaps, want 0", n)
	}
	got := storedHardening(t, r, w)
	if len(got.Status.Plan) != 0 {
		t.Errorf("status.plan = %+v, want no row: there is nothing to approve", got.Status.Plan)
	}
	if got.Status.Phase != v1alpha1.PhasePreviewed {
		t.Errorf("phase = %q, want Previewed", got.Status.Phase)
	}
	// The findings still report what was seen: silence is how the lie that a
	// namespace is hardened gets told (BR-04).
	var defaulted bool
	for _, f := range got.Status.Findings {
		if strings.Contains(f.Reason, "defaulted from limit") {
			defaulted = true
		}
	}
	if !defaulted {
		t.Errorf("findings = %+v, want the defaulted-from-limit observation reported", got.Status.Findings)
	}
}

func ptrTo[T any](v T) *T { return &v }
```

- [ ] **Step 10: Run it**

Run: `go test ./pkg/controller/ -run TestFullyHardenedTarget -v`
Expected: PASS.

- [ ] **Step 11: Write the unreadable-spec test (Review Focus 6)**

Append to `pkg/controller/hardening_test.go`:

```go
// Review Focus 6: a stored object whose spec will not convert into the typed
// struct — a quantity the schema admits but resource.Quantity refuses, or an
// object stored before the schema tightened. Returning the conversion error
// requeues forever behind a blank status, which is indistinguishable from an
// object the controller has never seen.
func TestUnreadableSpecIsRejectedNotRetriedForever(t *testing.T) {
	broken := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.GroupName + "/" + v1alpha1.Version,
		"kind":       v1alpha1.HardeningKind,
		"metadata": map[string]any{
			"name": "tenant-hardening", "namespace": "isolation-system", "uid": hardUID,
		},
		"spec": map[string]any{
			"namespaces": []any{"tenant-a"},
			// "10mm" has no valid quantity suffix: ParseQuantity refuses it,
			// so Quantity.UnmarshalJSON fails and the whole spec will not
			// convert.
			"resources": map[string]any{"requests": map[string]any{"cpu": "10mm", "memory": "32Mi"}},
		},
	}}

	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			v1alpha1.Resource:          v1alpha1.Kind + "List",
			v1alpha1.HardeningResource: v1alpha1.HardeningKind + "List",
		},
		broken,
	)

	if err := r.Reconcile(context.Background(), "isolation-system/tenant-hardening"); err != nil {
		t.Fatalf("Reconcile returned %v; an unreadable spec must be reported, not requeued forever", err)
	}

	u, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace("isolation-system").
		Get(context.Background(), "tenant-hardening", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the object back: %v", err)
	}
	status, _ := u.Object["status"].(map[string]any)
	if status == nil {
		t.Fatal("no status written; the object is indistinguishable from one never evaluated")
	}
	if status["phase"] != string(v1alpha1.PhaseRejected) {
		t.Errorf("phase = %v, want Rejected", status["phase"])
	}
	message, _ := status["message"].(string)
	if !strings.Contains(message, "spec cannot be read") {
		t.Errorf("message = %q, want it to name the cause", message)
	}

	// Nothing was read from the cluster and nothing was written to it.
	if n := len(r.Kube.(*fake.Clientset).Actions()); n != 0 {
		t.Errorf("%d cluster calls made for an object that cannot be planned", n)
	}
}
```

Add `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"` to this file's imports.

- [ ] **Step 12: Run it and check the package builds clean**

Run: `go test ./pkg/controller/ -run TestUnreadableSpec -v`
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add pkg/controller
git commit -m "test(controller): dry-run cache, per-target refusals, no-gap targets

Covers AC-10's unarmed half plus two cases no acceptance criterion names:
a fully hardened target issues no API call at all, and an object whose spec
will not convert is Rejected rather than requeued forever behind a blank
status."
```

---

### Task 8: Apply — per-target approval, partial failure and the phases (FR-04, FR-06, BR-07, AC-09, AC-13, AC-14)

The armed pass. Each target is decided on **its own hash**, never on a plan-wide one: a single plan-wide hash cannot converge in a live environment, because any CI deploy touching any workload in any of up to sixteen namespaces moves it.

**Distinguishing `Stale` from `Unapproved`** is resolved against the previously published `status.plan`, as BR-07 now specifies: if an earlier pass published a hash for **this target** and the operator approved **that** hash, a different current hash is `Stale`; otherwise it is `Unapproved`. Both rows of BR-07's table are claims about the past, and `approvedPlan` is a flat list of hashes with no target attached, so the published plan is the only record there is. With no such record — a first armed pass carrying hashes from elsewhere, or a cleared status — the target reports `Unapproved`.

**The phase does not rest on that distinction.** A row's _label_ needs the previous `status.plan` and degrades to `Unapproved` when it is missing; whether an approval pointed at anything real does not. `danglingApprovals` counts the approved hashes matching no row at all, and any dangling hash holds the object non-terminal on its own. Without that split, an object with one target patched and one approval that matched nothing folds to `Applied` — terminal — and that second approval is never looked at again.

**An apply always dry-runs first, in the same pass, cache or not** (FR-03, NFR-02).

**Never roll back.** A half-hardened namespace is not improved by un-hardening the half that worked. The retry recomputes, and because of BR-01 an already-patched target has no gaps left, so it addresses only what failed.

**Files:**

- Modify: `pkg/controller/execute.go` (replace the three stubs from Task 7)
- Test: `pkg/controller/hardening_test.go`

**Interfaces:**

- Consumes: everything from Task 7.
- Produces (replacing the stubs):
  - `func (r *HardeningReconciler) apply(...) v1alpha1.TargetStatus`
  - `func approvedEarlier(w *v1alpha1.WorkloadHardening, ref plan.Target) bool`
  - `func phaseFor(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) (v1alpha1.Phase, string)`
  - `func danglingApprovals(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) int`
  - `func carry(previous, rows []v1alpha1.TargetStatus) []v1alpha1.TargetStatus`
  - `func rowKey(row v1alpha1.TargetStatus) string`

- [ ] **Step 1: Write the failing per-target approval test (AC-09)**

Append to `pkg/controller/hardening_test.go`:

```go
// armed returns w with the given hashes approved, stored in the fake.
func arm(t *testing.T, r *HardeningReconciler, w *v1alpha1.WorkloadHardening, hashes ...string) *v1alpha1.WorkloadHardening {
	t.Helper()
	current := storedHardening(t, r, w)
	current.Spec.ApprovedPlan = hashes
	// The API server bumps metadata.generation on every write that changes
	// spec; the dynamic fake does not. FR-05's terminality gate compares it
	// against status.observedGeneration, so a test that left it alone could
	// never exercise a re-approval at all — the same class of gap as the fake
	// clientset ignoring DryRun. Status writes go through UpdateStatus and
	// correctly do not move it.
	current.Generation++
	u, err := v1alpha1.HardeningToUnstructured(current)
	if err != nil {
		t.Fatalf("HardeningToUnstructured: %v", err)
	}
	if _, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(w.Namespace).
		Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("arming the request: %v", err)
	}
	return current
}

// AC-09: an approved target is patched while an unrelated workload appearing
// in the same namespace is reported Unapproved without blocking it or the
// phase; a target whose own change moved is Stale and holds the object in
// PartiallyApplied.
func TestPerTargetApproval(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	// Preview, then approve api's hash.
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	apiHash := rowFor(storedHardening(t, r, w), "Deployment", "api").Hash
	if apiHash == "" {
		t.Fatal("no hash published for api")
	}
	arm(t, r, w, apiHash)

	// A workload nobody approved appears in the same namespace.
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").
		Create(context.Background(), deployment("tenant-a", "newcomer"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding the newcomer: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase = %q (%s), want Applied: Unapproved is not a failure", got.Status.Phase, got.Status.Message)
	}
	if api := rowFor(got, "Deployment", "api"); api.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("api outcome = %q, want Patched", api.Outcome)
	}
	newcomer := rowFor(got, "Deployment", "newcomer")
	if newcomer.Outcome != v1alpha1.OutcomeUnapproved {
		t.Errorf("newcomer outcome = %q, want Unapproved", newcomer.Outcome)
	}
	if newcomer.Hash == "" {
		t.Error("the newcomer's hash was not published; the operator cannot approve what it cannot see")
	}
	if !strings.Contains(got.Status.Message, "unapproved") {
		t.Errorf("message = %q, want the unapproved count named (FR-06)", got.Status.Message)
	}

	// The approved target was patched for real, and the unapproved one was
	// never written to at all — not even a dry-run.
	patched := storedTemplate(t, r, "tenant-a", "api")
	if patched.Spec.Template.Spec.SecurityContext == nil || patched.Spec.Template.Spec.SecurityContext.RunAsNonRoot == nil {
		t.Error("api was not patched")
	}
	for _, p := range patchActions(t, r) {
		if p.Name == "newcomer" {
			t.Error("the unapproved target was sent to the API server")
		}
	}
	untouched := storedTemplate(t, r, "tenant-a", "newcomer")
	if untouched.Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Error("the unapproved target was annotated")
	}
}

// AC-09, the Stale half: a target whose own change moved since approval is not
// patched, its new hash is published for re-approval, and the object is held
// in PartiallyApplied, which is non-terminal and re-evaluated until the
// approval is updated.
func TestStaleApprovalHoldsPartiallyApplied(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	stale := rowFor(storedHardening(t, r, w), "Deployment", "api").Hash
	arm(t, r, w, stale)

	// Someone edits the workload in a way that changes its gaps.
	d := storedTemplate(t, r, "tenant-a", "api")
	d.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("50m"),
	}
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").Update(context.Background(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("editing api: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	// Stale is not retryable: recomputing produces the same refusal until a
	// human updates the approval, so spinning the backoff would be pointless.
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q, want PartiallyApplied", got.Status.Phase)
	}
	api := rowFor(got, "Deployment", "api")
	if api.Outcome != v1alpha1.OutcomeStale {
		t.Errorf("api outcome = %q, want Stale", api.Outcome)
	}
	if api.Hash == stale || api.Hash == "" {
		t.Errorf("hash = %q, want the new one published for re-approval", api.Hash)
	}
	if !strings.Contains(api.Reason, api.Hash) {
		t.Errorf("reason = %q, want it to name the hash to re-approve", api.Reason)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches for a stale target, want 0", n)
	}

	// Re-approving the new hash converges.
	arm(t, r, w, api.Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("re-approved apply: %v", err)
	}
	if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after re-approval = %q (%s), want Applied", got.Status.Phase, got.Status.Message)
	}
}

// An apply always dry-runs the patch in the same pass, cache or not: the cache
// key is the target's own change, which does not move when a webhook is
// installed, a namespace gains a Pod Security label or a LimitRange appears
// (AC-10's apply half, FR-03, NFR-02).
func TestApplyAlwaysDryRunsFirst(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	// The preview cached a clean dry-run for this exact change.
	arm(t, r, w, rowFor(storedHardening(t, r, w), "Deployment", "api").Hash)

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	patches := patchActions(t, r)
	if len(patches) != 2 {
		t.Fatalf("issued %d patches, want a dry-run then the write", len(patches))
	}
	if !slices.Contains(patches[0].PatchOptions.DryRun, metav1.DryRunAll) {
		t.Error("the first patch was not a dry-run; no write may precede a dry-run of that same patch")
	}
	if len(patches[1].PatchOptions.DryRun) != 0 {
		t.Error("the second patch was still a dry-run; nothing was written")
	}
	// Both carry the same body, so the thing verified is the thing written.
	if string(patches[0].Patch) != string(patches[1].Patch) {
		t.Error("the dry-run and the write carried different bodies")
	}
}

// AC-11 on the wire: every applied patch carries the annotation in the same
// request, recording leaf paths and the values written.
func TestProvenanceRidesInTheSameRequest(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	arm(t, r, w, rowFor(storedHardening(t, r, w), "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	for _, p := range patchActions(t, r) {
		if !strings.Contains(string(p.Patch), v1alpha1.FilledAnnotation) {
			t.Errorf("a patch of %s carried no provenance annotation:\n%s", p.Name, p.Patch)
		}
	}

	filled := storedTemplate(t, r, "tenant-a", "api").Annotations[v1alpha1.FilledAnnotation]
	if filled == "" {
		t.Fatal("no provenance annotation on the patched target")
	}
	for _, want := range []string{
		"spec.template.spec.securityContext.runAsNonRoot=true",
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation=false",
		"spec.template.spec.containers[app].securityContext.capabilities.drop=[ALL]",
		"spec.template.spec.containers[app].resources.requests.cpu=10m",
		"spec.template.spec.containers[app].resources.requests.memory=32Mi",
	} {
		if !strings.Contains(filled, want) {
			t.Errorf("annotation missing %q:\n%s", want, filled)
		}
	}
	// Leaf paths, so an undo removes exactly what was added rather than a
	// block a human may have written afterwards (BR-08).
	for _, line := range strings.Split(filled, "\n") {
		path, _, _ := strings.Cut(line, "=")
		if path == "spec.template.spec.containers[app].resources" || path == "spec.template.spec.securityContext" {
			t.Errorf("a non-leaf path reached the annotation: %q", line)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./pkg/controller/ -run 'TestPerTargetApproval|TestStaleApproval|TestApplyAlwaysDryRuns|TestProvenanceRides' -v`
Expected: FAIL — every target reports `Unapproved` and the phase is `Previewed`, because `apply` and `phaseFor` are still Task 7's stubs.

- [ ] **Step 3: Write the implementation**

Replace the three stubs at the bottom of `pkg/controller/execute.go`:

```go
// apply patches a target whose own change hash the operator approved (BR-07).
//
// Each target is decided on its own hash, never on a plan-wide one. A single
// plan-wide hash cannot converge in a live environment: any CI deploy touching
// any workload in any of up to sixteen namespaces moves it, so the operator
// re-copies the hash and is stale again before the write lands.
func (r *HardeningReconciler) apply(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	body []byte,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	switch {
	case w.Approved(row.Hash):
		// Patched below.
	case approvedEarlier(w, t.Ref):
		// The operator approved a hash for this target, and it no longer
		// describes it. Publish the new one for re-approval (BR-07).
		row.Outcome = v1alpha1.OutcomeStale
		row.Reason = "the approved change no longer describes this target; re-approve " + row.Hash
		logger.Info("Stale", "target", t.Ref.String(), "hash", row.Hash)
		return row
	default:
		// Appeared after the approval. Not a failure, and it never triggers a
		// retry: approving a subset is the expected use of a per-target gate
		// (FR-04, FR-06).
		row.Outcome = v1alpha1.OutcomeUnapproved
		row.Reason = "not in spec.approvedPlan; add " + row.Hash + " to approve it"
		return row
	}

	// No write before a dry-run of that same patch, in the same pass, has been
	// accepted. The cache is not consulted here: its key is the target's own
	// change, which does not move when a webhook is installed, a namespace
	// gains a Pod Security label or a LimitRange appears, so a cached
	// acceptance says nothing about whether this write will be accepted now
	// (FR-03, NFR-02).
	if err := t.patch(ctx, body, dryRun()); err != nil {
		logger.Info("Dry-run rejected", "target", t.Ref.String(), "reason", err.Error())
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = "dry-run rejected: " + err.Error()
		r.forget(w, t.Ref)
		return row
	}

	if err := t.patch(ctx, body, metav1.PatchOptions{}); err != nil {
		// Keep what succeeded elsewhere and never roll back: a half-hardened
		// namespace is not improved by un-hardening the half that worked
		// (FR-04). An API error is never assumed to have landed.
		logger.Error(err, "Patch failed", "target", t.Ref.String(), "hash", row.Hash)
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = err.Error()
		return row
	}

	row.Outcome = v1alpha1.OutcomePatched
	logger.Info("Patched", "target", t.Ref.String(), "hash", row.Hash, "fields", row.Fields, "pods", row.Pods)
	return row
}

// approvedEarlier reports whether an earlier pass published a hash for this
// target that the operator then approved.
//
// That is what separates Stale — the operator approved a change that no longer
// exists — from Unapproved, a target that appeared after the approval. The
// previous status.plan is the only record of what was published, so it is what
// the distinction is drawn from (BR-07).
//
// With no such record — a first armed pass carrying hashes copied from
// elsewhere, or a cleared status — this returns false and the target reports
// Unapproved. BR-07 sanctions that degradation and bounds it to the label: the
// phase turns on unmatched hashes (FR-06), which need no history.
func approvedEarlier(w *v1alpha1.WorkloadHardening, ref plan.Target) bool {
	for _, row := range w.Status.Plan {
		if row.Namespace == ref.Namespace && row.Kind == ref.Kind && row.Name == ref.Name {
			return w.Approved(row.Hash)
		}
	}
	return false
}

// rowKey orders and identifies a plan row: namespace, kind, name — the same
// deterministic order execution uses (FR-04).
func rowKey(row v1alpha1.TargetStatus) string {
	return row.Namespace + "/" + row.Kind + "/" + row.Name
}

// carry brings forward the Patched rows of the previous status for targets the
// recomputed plan no longer names.
//
// An already-patched target has no gaps left (BR-01), so it drops out of the
// plan entirely — and an Applied object whose status showed an empty plan
// would tell an operator nothing about what it did. It is also what lets
// phaseFor tell "everything approved has landed" from "the approval matched
// nothing".
func carry(previous, rows []v1alpha1.TargetStatus) []v1alpha1.TargetStatus {
	named := make(map[string]bool, len(rows))
	for _, row := range rows {
		named[rowKey(row)] = true
	}
	for _, row := range previous {
		if row.Outcome == v1alpha1.OutcomePatched && !named[rowKey(row)] {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b v1alpha1.TargetStatus) int {
		return strings.Compare(rowKey(a), rowKey(b))
	})
	return rows
}

// danglingApprovals counts the hashes in spec.approvedPlan that match no row in
// the status at all — neither a target that has gaps now, nor one this object
// patched earlier and carried forward.
//
// This is the history-free half of BR-07's gate, and it is deliberately kept
// out of the history-dependent path. Telling Stale from Unapproved needs the
// previously published status.plan and degrades to Unapproved when that is
// gone; whether an approval pointed at anything real does not need history at
// all. Folding the phase on this instead of on the Stale count means a cleared
// status costs a row's label and never lets the object go terminal with the
// operator's approval silently unapplied.
func danglingApprovals(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) int {
	present := make(map[string]bool, len(rows))
	for _, row := range rows {
		present[row.Hash] = true
	}
	var n int
	for _, h := range w.Spec.ApprovedPlan {
		if !present[h] {
			n++
		}
	}
	return n
}

// phaseFor folds the per-target outcomes into the object's phase (FR-06).
func phaseFor(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) (v1alpha1.Phase, string) {
	var patched, failed, stale, unapproved int
	for _, row := range rows {
		switch row.Outcome {
		case v1alpha1.OutcomePatched:
			patched++
		case v1alpha1.OutcomeFailed:
			failed++
		case v1alpha1.OutcomeStale:
			stale++
		case v1alpha1.OutcomeUnapproved:
			unapproved++
		}
	}

	if !w.Armed() {
		if failed > 0 {
			return v1alpha1.PhasePreviewed, fmt.Sprintf(
				"%d targets would change; %d were refused by the dry-run", len(rows), failed)
		}
		if len(rows) == 0 {
			return v1alpha1.PhasePreviewed, "no gaps found"
		}
		return v1alpha1.PhasePreviewed, fmt.Sprintf(
			"%d targets would change; approve them by copying their hashes into spec.approvedPlan", len(rows))
	}

	dangling := danglingApprovals(w, rows)

	switch {
	case failed > 0 || stale > 0:
		// Stale is a failure of a different kind — the operator approved a
		// change that no longer exists — so it holds the object in
		// PartiallyApplied, which is non-terminal and re-evaluated until the
		// approval is updated (FR-06).
		return v1alpha1.PhasePartiallyApplied, fmt.Sprintf(
			"%d patched, %d failed, %d stale, %d unapproved", patched, failed, stale, unapproved)
	case dangling == len(w.Spec.ApprovedPlan) && patched == 0:
		// Nothing the operator approved exists. Applied is terminal, so
		// reporting it here would strand the request with the approval having
		// done nothing, forever, and no resync would ever look again.
		return v1alpha1.PhasePartiallyApplied, fmt.Sprintf(
			"none of the %d approved hashes matches a target with gaps; %d targets are unapproved",
			len(w.Spec.ApprovedPlan), unapproved)
	case dangling > 0:
		// Some landed and some pointed at nothing. This is the case a count of
		// Stale rows misses: the successful patches would otherwise carry the
		// object to Applied, which is terminal, and the approvals that matched
		// nothing would never be looked at again.
		return v1alpha1.PhasePartiallyApplied, fmt.Sprintf(
			"%d patched; %d of the %d approved hashes matches no target with gaps",
			patched, dangling, len(w.Spec.ApprovedPlan))
	case patched > 0:
		// Unapproved is not a failure. An object whose approved targets all
		// patched is Applied however many targets it left alone (FR-06).
		return v1alpha1.PhaseApplied, fmt.Sprintf("%d patched, %d left unapproved", patched, unapproved)
	default:
		// Armed, nothing approved is missing, and nothing was patched: every
		// approved hash names a row the plan still reports but cannot act on —
		// a refused target (BR-04), for instance. Non-terminal, so it recovers
		// by itself if the refusal clears.
		return v1alpha1.PhasePartiallyApplied, fmt.Sprintf(
			"%d approved hashes matched no patchable target; %d targets are unapproved",
			len(w.Spec.ApprovedPlan), unapproved)
	}
}
```

Add `"slices"` and `"strings"` to `execute.go`'s import block, and `"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"` if it is not already there.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/controller/ -run 'TestPerTargetApproval|TestStaleApproval|TestApplyAlwaysDryRuns|TestProvenanceRides' -v`
Expected: PASS, all four.

- [ ] **Step 5: Commit**

```bash
git add pkg/controller
git commit -m "feat(controller): per-target approval, apply and the phase fold

Each target is decided on its own hash. Stale is told from Unapproved by the
previously published status.plan: an approval the operator gave for this
target that no longer describes it is Stale; anything else is Unapproved,
which is not a failure and never triggers a retry.

The phase folds on danglingApprovals rather than on the Stale count, so an
approval that matched nothing holds the object non-terminal even when other
targets patched successfully, and a cleared status costs a label rather than
a stranded request."
```

- [ ] **Step 6: Write the partial-failure test (AC-13)**

Append to `pkg/controller/hardening_test.go`:

```go
// AC-13: partial failure keeps what succeeded, reports PartiallyApplied, and
// converges on retry without re-patching what already landed.
//
// Never roll back: a half-hardened namespace is not improved by un-hardening
// the half that worked (FR-04).
func TestPartialFailureKeepsWhatSucceededAndConverges(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)
	arm(t, r, w,
		rowFor(previewed, "Deployment", "api").Hash,
		rowFor(previewed, "Deployment", "web").Hash)

	// web's real write fails; its dry-run does not, so the pass gets as far as
	// attempting the write and then has to keep api's success.
	failing := true
	r.Kube.(*fake.Clientset).PrependReactor("patch", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p := a.(k8stesting.PatchActionImpl)
		if failing && p.Name == "web" && len(p.PatchOptions.DryRun) == 0 {
			return true, nil, apierrors.NewInternalError(errors.New("etcd is unhappy"))
		}
		return false, nil, nil
	})

	err := r.Reconcile(context.Background(), hardeningKey(w))
	if err == nil {
		t.Error("a failed patch must return an error so the key is requeued")
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q (%s), want PartiallyApplied", got.Status.Phase, got.Status.Message)
	}
	if api := rowFor(got, "Deployment", "api"); api.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("api outcome = %q, want Patched: what succeeded is kept", api.Outcome)
	}
	if web := rowFor(got, "Deployment", "web"); web.Outcome != v1alpha1.OutcomeFailed {
		t.Errorf("web outcome = %q, want Failed", web.Outcome)
	}
	if storedTemplate(t, r, "tenant-a", "api").Annotations[v1alpha1.FilledAnnotation] == "" {
		t.Error("api's patch was rolled back")
	}

	// The retry recomputes. api has no gaps left, so it is not re-patched;
	// only web is addressed.
	failing = false
	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("retry: %v", err)
	}

	for _, p := range patchActions(t, r) {
		if p.Name == "api" {
			t.Error("api was patched again; an already-patched target has no gaps left (BR-01)")
		}
	}

	converged := storedHardening(t, r, w)
	if converged.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after retry = %q (%s), want Applied", converged.Status.Phase, converged.Status.Message)
	}
	// Both are still reported: an Applied object whose plan is empty tells an
	// operator nothing about what it did.
	for _, name := range []string{"api", "web"} {
		if row := rowFor(converged, "Deployment", name); row.Outcome != v1alpha1.OutcomePatched {
			t.Errorf("%s outcome after convergence = %q, want Patched", name, row.Outcome)
		}
		if storedTemplate(t, r, "tenant-a", name).Annotations[v1alpha1.FilledAnnotation] == "" {
			t.Errorf("%s carries no provenance annotation", name)
		}
	}
}

// AC-14: an Applied object issues no API calls on resync, including one that
// reached Applied with targets left Unapproved; a Rejected one is re-evaluated
// and reaches Previewed once the cause clears.
func TestTerminalAndRecoveringPhases(t *testing.T) {
	t.Run("Applied is terminal, including with unapproved targets", func(t *testing.T) {
		w := hardening("tenant-a")
		r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
		r.Dyn = hardeningDynClient(t, w)
		dryRunGuard(t, r.Kube.(*fake.Clientset))

		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("preview: %v", err)
		}
		// Approve only one of the two.
		arm(t, r, w, rowFor(storedHardening(t, r, w), "Deployment", "api").Hash)
		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("apply: %v", err)
		}
		applied := storedHardening(t, r, w)
		if applied.Status.Phase != v1alpha1.PhaseApplied {
			t.Fatalf("phase = %q (%s), want Applied", applied.Status.Phase, applied.Status.Message)
		}
		if rowFor(applied, "Deployment", "web").Outcome != v1alpha1.OutcomeUnapproved {
			t.Fatal("web should have been left Unapproved")
		}

		r.Kube.(*fake.Clientset).ClearActions()
		r.Dyn.(*dynamicfake.FakeDynamicClient).ClearActions()
		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("resync: %v", err)
		}

		// No workload call of any kind: not a list, not a get, not a patch.
		if n := len(r.Kube.(*fake.Clientset).Actions()); n != 0 {
			t.Errorf("%d cluster calls on an Applied resync, want 0", n)
		}
		// The request itself is read — that is unavoidable — but never written.
		for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
			switch a.GetVerb() {
			case "get", "list", "watch":
			default:
				t.Errorf("unexpected write on an Applied resync: %s %s", a.GetVerb(), a.GetSubresource())
			}
		}
	})

	t.Run("Rejected recovers once the cause clears", func(t *testing.T) {
		w := hardening("tenant-a", "tenant-b")
		r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
		r.Dyn = hardeningDynClient(t, w)
		dryRunGuard(t, r.Kube.(*fake.Clientset))

		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseRejected {
			t.Fatalf("phase = %q, want Rejected", got.Status.Phase)
		}

		if _, err := r.Kube.CoreV1().Namespaces().Create(context.Background(), ns("tenant-b"), metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating tenant-b: %v", err)
		}
		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("recovery pass: %v", err)
		}
		got := storedHardening(t, r, w)
		if got.Status.Phase != v1alpha1.PhasePreviewed {
			t.Errorf("phase = %q (%s), want Previewed: Rejected is not terminal", got.Status.Phase, got.Status.Message)
		}
		if len(got.Status.Plan) == 0 {
			t.Error("no plan published after recovery")
		}
	})
}
```

- [ ] **Step 7: Run them**

Run: `go test ./pkg/controller/ -run 'TestPartialFailure|TestTerminalAndRecovering' -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/controller
git commit -m "test(controller): partial failure, convergence and the terminal phases

Covers AC-13 and AC-14. The retry recomputes rather than replaying, so an
already-patched target has no gaps left and only the failure is addressed."
```

- [ ] **Step 9: Write the unmatched-approval and re-approval tests (Review Focus 3, AC-17, AC-18)**

Append to `pkg/controller/hardening_test.go`:

```go
// Review Focus 3: an approvedPlan holding hashes that match nothing — a
// copy-paste from a stale preview, or an approval landing after someone else
// patched the targets. Every target is then Unapproved, nothing fails, and
// nothing is Stale. FR-06's literal reading makes that Applied, which is
// terminal, so the operator's approval would silently do nothing forever and
// no resync would ever look again.
func TestApprovalMatchingNothingIsNotTerminal(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	// A hash that is well-formed and belongs to nothing.
	arm(t, r, w, "deadbeef0000")

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase == v1alpha1.PhaseApplied {
		t.Fatal("phase = Applied: a terminal phase would strand the request with the approval having done nothing")
	}
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q, want PartiallyApplied", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "none of the 1 approved hashes") {
		t.Errorf("message = %q, want it to say the approval matched nothing", got.Status.Message)
	}
	if row := rowFor(got, "Deployment", "api"); row.Outcome != v1alpha1.OutcomeUnapproved {
		t.Errorf("api outcome = %q, want Unapproved", row.Outcome)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches, want 0", n)
	}

	// Non-terminal, so correcting the approval still works.
	arm(t, r, w, rowFor(got, "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("corrected apply: %v", err)
	}
	if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after correction = %q (%s), want Applied", got.Status.Phase, got.Status.Message)
	}
}

// The half of Review Focus 3 that a count of Stale rows does not catch: one
// approval lands and another matches nothing. Folding the phase on patched >
// 0 gives Applied, which is terminal, and the second approval is never looked
// at again — so the phase folds on danglingApprovals instead.
func TestDanglingApprovalOutlivesASuccessfulPatch(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)

	// One real approval, one that belongs to nothing.
	arm(t, r, w, rowFor(previewed, "Deployment", "api").Hash, "deadbeef0000")

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if row := rowFor(got, "Deployment", "api"); row.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("api outcome = %q, want Patched: the real approval must still land", row.Outcome)
	}
	if got.Status.Phase == v1alpha1.PhaseApplied {
		t.Fatal("phase = Applied: terminal, so the approval that matched nothing is never revisited")
	}
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q (%s), want PartiallyApplied", got.Status.Phase, got.Status.Message)
	}
	if !strings.Contains(got.Status.Message, "1 of the 2 approved hashes") {
		t.Errorf("message = %q, want it to name the dangling approval", got.Status.Message)
	}

	// Dropping the bad hash settles it: api stays patched and carries forward.
	arm(t, r, w, rowFor(got, "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("corrected apply: %v", err)
	}
	if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after correction = %q (%s), want Applied", got.Status.Phase, got.Status.Message)
	}
}

// AC-18: Applied is terminal only until the approval changes. approvedPlan is
// the only mutable field in spec, so an operator extending it — FR-01's next
// batch, or the rest of a subset deliberately approved earlier (BR-07) — must
// be acted on. A resync that moves nothing still issues no API calls (AC-14).
func TestApprovalExtendedAfterApplied(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)

	// Approve a subset. BR-07 calls this the normal way to use the gate.
	arm(t, r, w, rowFor(previewed, "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	applied := storedHardening(t, r, w)
	if applied.Status.Phase != v1alpha1.PhaseApplied {
		t.Fatalf("phase = %q (%s), want Applied", applied.Status.Phase, applied.Status.Message)
	}
	if applied.Status.ObservedGeneration != applied.Generation {
		t.Fatalf("observedGeneration = %d, want %d: the gate compares these two",
			applied.Status.ObservedGeneration, applied.Generation)
	}

	// A resync moves no generation, so the object stays terminal and silent.
	before := len(r.Kube.(*fake.Clientset).Actions())
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if after := len(r.Kube.(*fake.Clientset).Actions()); after != before {
		t.Errorf("resync issued %d API calls, want 0 (AC-14)", after-before)
	}

	// Extending the approval moves generation, so the terminal phase gives way.
	patchesBefore := len(patchActions(t, r))
	arm(t, r, w, rowFor(applied, "Deployment", "api").Hash, rowFor(previewed, "Deployment", "web").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("second batch: %v", err)
	}

	got := storedHardening(t, r, w)
	if row := rowFor(got, "Deployment", "web"); row.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("web outcome = %q, want Patched: the extended approval was ignored", row.Outcome)
	}
	// api has no gaps left (BR-01), so it is not patched a second time.
	for _, a := range patchActions(t, r)[patchesBefore:] {
		if a.GetName() == "api" {
			t.Error("api was patched again; BR-01 means an already-patched target has no gaps left")
		}
	}
}
```

- [ ] **Step 10: Run it**

Run: `go test ./pkg/controller/ -run 'TestApprovalMatchingNothing|TestDanglingApproval|TestApprovalExtended' -v`
Expected: PASS, all three. If `TestDanglingApprovalOutlivesASuccessfulPatch` reports `Applied`, `phaseFor` is still folding on `patched > 0` ahead of the dangling count. If `TestApprovalExtendedAfterApplied` leaves `web` unpatched, the terminality check is still looking at the phase alone rather than at `observedGeneration` too.

- [ ] **Step 11: Write the vanishing-namespace test (Review Focus 5)**

Append to `pkg/controller/hardening_test.go`:

```go
// Review Focus 5: a namespace that vanishes between validation and apply.
// Validation reads namespaces first and is all-or-nothing; the workload List
// that follows returns an empty list, not NotFound, once the namespace is
// gone. The request must not report Applied claiming it hardened a namespace
// that no longer exists.
func TestNamespaceVanishingBetweenValidationAndApply(t *testing.T) {
	w := hardening("tenant-a", "tenant-b")
	r := newHardener(
		ns("tenant-a"), deployment("tenant-a", "api"),
		ns("tenant-b"), deployment("tenant-b", "gone-soon"),
	)
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)
	arm(t, r, w, rowFor(previewed, "Deployment", "gone-soon").Hash)

	// tenant-b's namespace object is still there — validation passes — but
	// its contents are gone, so the List comes back empty rather than
	// erroring. That is what a namespace being reaped looks like mid-pass.
	if err := r.Kube.AppsV1().Deployments("tenant-b").
		Delete(context.Background(), "gone-soon", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting gone-soon: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase == v1alpha1.PhaseApplied {
		t.Fatal("phase = Applied: nothing was patched, and Applied is terminal")
	}
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q (%s), want PartiallyApplied", got.Status.Phase, got.Status.Message)
	}
	// The vanished target is simply absent from the recomputed plan, not
	// reported as Failed: it was never patched and nothing went wrong.
	if row := rowFor(got, "Deployment", "gone-soon"); row.Outcome != "" {
		t.Errorf("gone-soon outcome = %q, want it absent from the recomputed plan", row.Outcome)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches, want 0", n)
	}

	// And a namespace that disappears entirely rejects the whole object again,
	// because validation is all-or-nothing (FR-05).
	if err := r.Kube.CoreV1().Namespaces().Delete(context.Background(), "tenant-b", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting tenant-b: %v", err)
	}
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("post-deletion pass: %v", err)
	}
	after := storedHardening(t, r, w)
	if after.Status.Phase != v1alpha1.PhaseRejected {
		t.Errorf("phase = %q, want Rejected once the namespace is gone", after.Status.Phase)
	}
	if !strings.Contains(after.Status.Message, "tenant-b") {
		t.Errorf("message = %q, want it to name the missing namespace", after.Status.Message)
	}
	// tenant-a was not patched either: the operator named two namespaces and
	// gets both or neither.
	if storedTemplate(t, r, "tenant-a", "api").Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Error("tenant-a was patched although the object is Rejected")
	}
}
```

- [ ] **Step 12: Run the whole package and check coverage**

Run: `go test ./pkg/... -race -cover -v`
Expected: PASS everywhere, `pkg/apis/v1alpha1`, `pkg/plan`, `pkg/policy` and `pkg/controller` all ≥90%. 001's tests must still pass untouched.

- [ ] **Step 13: Commit**

```bash
git add pkg/controller
git commit -m "test(controller): unmatched approvals and a vanishing namespace

Two conditions no acceptance criterion names. An approvedPlan matching
nothing must not reach the terminal Applied phase, and a namespace whose
contents disappear mid-pass must not be reported as hardened."
```

---

### Task 9: Wiring, RBAC, manifests, live verification and README (FR-05, NFR-03, NFR-06, AC-15)

The reconciler is finished and tested. This is the plumbing that calls it, the permissions it needs, and the only evidence that a patched workload still runs — because fake clients do not run Kubernetes' defaulting and are not evidence of anything AC-03, AC-05 or AC-06 assert.

**One queue, two resources.** `Controller.reconcile` becomes a map keyed by resource name, and queue keys carry the resource they came from: `workloadhardenings|isolation-system/tenant-hardening`. This is the only 001 file this plan changes, and it changes three of 001's existing tests with it — they are listed explicitly below.

**RBAC is where 001's lesson applies.** `patch` is a distinct verb from `update`, and only an in-cluster run catches a missing one. The workload rules grant `get`, `list` and `patch` — no `update`, no `delete`, no `create`. No `resourcequotas` at all (BR-06, D-15). No `update` on `workloadhardenings` itself, only on its status, because there is no finalizer to persist.

**Files:**

- Modify: `pkg/controller/controller.go`, `pkg/controller/controller_test.go`, `cmd/main/main.go`, `deploy/rbac.yaml`, `Makefile`, `README.md`
- Create: `deploy/samples/workloads-hardening.yaml`, `deploy/samples/hardening.yaml`, `hack/verify-hardening.sh`

**Interfaces:**

- Consumes: `HardeningReconciler.Reconcile` (Task 7), `Reconciler.Reconcile` (001), `v1alpha1.Resource`, `v1alpha1.HardeningResource` (Task 1).
- Produces:
  - `func New(kube kubernetes.Interface, dyn dynamic.Interface, iso *Reconciler, hardening *HardeningReconciler, resync time.Duration) (*Controller, error)` — **signature change**
  - `func queueKey(resource, namespace, name string) string`
  - `func splitQueueKey(key string) (resource, object string, err error)`
  - `make samples-hardening`, `make verify-hardening`

- [ ] **Step 1: Write the failing queue-key test**

Append to `pkg/controller/controller_test.go`:

```go
// One queue serves two custom resources, so a key has to carry the resource it
// came from. A bare "namespace/name" would send a WorkloadHardening to the
// NetworkIsolation reconciler, which would report it NotFound and forget it.
func TestQueueKeyRoundTrip(t *testing.T) {
	key := queueKey(v1alpha1.HardeningResource.Resource, "isolation-system", "tenant-hardening")
	if key != "workloadhardenings|isolation-system/tenant-hardening" {
		t.Errorf("queueKey = %q", key)
	}

	resource, object, err := splitQueueKey(key)
	if err != nil {
		t.Fatalf("splitQueueKey: %v", err)
	}
	if resource != "workloadhardenings" || object != "isolation-system/tenant-hardening" {
		t.Errorf("splitQueueKey = (%q, %q)", resource, object)
	}

	for _, bad := range []string{"", "isolation-system/gw-dash", "|isolation-system/gw-dash", "networkisolations|"} {
		if _, _, err := splitQueueKey(bad); err == nil {
			t.Errorf("splitQueueKey(%q) accepted a malformed key", bad)
		}
	}
}

// Each resource reaches its own reconciler, and an unknown one is dropped
// rather than retried forever against a reconciler that cannot serve it.
func TestProcessNextRoutesByResource(t *testing.T) {
	var served []string
	c := &Controller{
		queue: &fakeQueue{items: []string{
			queueKey("networkisolations", "isolation-system", "gw-dash"),
			queueKey("workloadhardenings", "isolation-system", "tenant-hardening"),
			"unknownresource|isolation-system/whatever",
		}},
		reconcile: map[string]func(context.Context, string) error{
			"networkisolations": func(_ context.Context, key string) error {
				served = append(served, "iso:"+key)
				return nil
			},
			"workloadhardenings": func(_ context.Context, key string) error {
				served = append(served, "hardening:"+key)
				return nil
			},
		},
	}

	for range 3 {
		if !c.processNext(context.Background()) {
			t.Fatal("processNext returned false while the queue still held items")
		}
	}

	want := []string{"iso:isolation-system/gw-dash", "hardening:isolation-system/tenant-hardening"}
	if strings.Join(served, ",") != strings.Join(want, ",") {
		t.Errorf("served = %v, want %v", served, want)
	}
	q := c.queue.(*fakeQueue)
	if len(q.requeued) != 0 {
		t.Errorf("requeued = %v; an unroutable key must be dropped, not retried forever", q.requeued)
	}
	if len(q.done) != 3 {
		t.Errorf("done = %v, want every key released exactly once", q.done)
	}
}
```

Add `"strings"` to this file's imports.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/controller/ -run 'TestQueueKey|TestProcessNextRoutes' -v`
Expected: FAIL — `undefined: queueKey`, and `cannot use map[string]func(...) as func(...) value in struct literal` for `Controller.reconcile`.

- [ ] **Step 3: Rewrite the queue plumbing**

In `pkg/controller/controller.go`, change the struct, `New`, `processNext`, `enqueue` and `enqueuePolicy`. Everything else in the file — `Run`, `runWorker`, `ownerKey` — is unchanged.

```go
// Controller watches both custom resources and the policies 001 owns, and
// feeds their keys to the right reconciler through one rate-limited queue and
// one worker. Hardening shares the binary, the queue and the worker with
// isolation (FR-05).
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]
	// reconcile maps a resource name to the reconciler that serves it. One
	// queue, two resources, so a key has to say which it came from.
	reconcile map[string]func(context.Context, string) error
	start     []func(<-chan struct{})
	synced    []cache.InformerSynced
}

// queueKey namespaces a work item by the resource it came from.
func queueKey(resource, namespace, name string) string {
	return resource + "|" + namespace + "/" + name
}

// splitQueueKey undoes queueKey.
func splitQueueKey(key string) (resource, object string, err error) {
	resource, object, found := strings.Cut(key, "|")
	if !found || resource == "" || object == "" {
		return "", "", fmt.Errorf("malformed queue key %q", key)
	}
	return resource, object, nil
}

// New wires the informers.
//
// Both custom resources share one dynamic factory: watching a second resource
// costs a second informer, not a second process, a second queue or a second
// worker. Only policies carrying 001's operation label are watched — hardening
// has no workload informer at all, deliberately, because it does not own the
// fields it writes and a reconcile loop would overwrite a deliberate later
// change and restart pods to do it (FR-05, D-02).
func New(
	kube kubernetes.Interface,
	dyn dynamic.Interface,
	iso *Reconciler,
	hardening *HardeningReconciler,
	resync time.Duration,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		reconcile: map[string]func(context.Context, string) error{
			v1alpha1.Resource.Resource:          iso.Reconcile,
			v1alpha1.HardeningResource.Resource: hardening.Reconcile,
		},
	}

	crFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, resync, metav1.NamespaceAll, nil)
	for _, gvr := range []schema.GroupVersionResource{v1alpha1.Resource, v1alpha1.HardeningResource} {
		resource := gvr.Resource
		informer := crFactory.ForResource(gvr).Informer()
		if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { c.enqueue(resource, obj) },
			UpdateFunc: func(_, obj any) { c.enqueue(resource, obj) },
			DeleteFunc: func(obj any) { c.enqueue(resource, obj) },
		}); err != nil {
			return nil, err
		}
		c.synced = append(c.synced, informer.HasSynced)
	}

	polFactory := informers.NewSharedInformerFactoryWithOptions(kube, resync,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.LabelSelector = v1alpha1.OperationLabel
		}))
	polInformer := polFactory.Networking().V1().NetworkPolicies().Informer()
	if _, err := polInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj any) { c.enqueuePolicy(obj) },
		DeleteFunc: func(obj any) { c.enqueuePolicy(obj) },
	}); err != nil {
		return nil, err
	}
	c.synced = append(c.synced, polInformer.HasSynced)

	c.start = []func(<-chan struct{}){crFactory.Start, polFactory.Start}
	return c, nil
}

func (c *Controller) processNext(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	resource, object, err := splitQueueKey(key)
	if err != nil {
		// Nothing can serve it, so retrying is an infinite loop over a
		// programming error.
		runtime.HandleError(err)
		c.queue.Forget(key)
		return true
	}
	reconcile, ok := c.reconcile[resource]
	if !ok {
		runtime.HandleError(fmt.Errorf("no reconciler for resource %q", resource))
		c.queue.Forget(key)
		return true
	}

	if err := reconcile(ctx, object); err != nil {
		// Retry indefinitely: dropping the key would silently abandon an
		// isolation request, a half-finished cleanup or a half-applied
		// hardening plan.
		runtime.HandleErrorWithLogger(klog.FromContext(ctx), err, "Reconcile failed, retrying", "object", key)
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) enqueue(resource string, obj any) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(resource + "|" + key)
}

// enqueuePolicy maps a policy event back to the NetworkIsolation that owns it.
// Hardening writes no policies, so a policy event never reaches it.
func (c *Controller) enqueuePolicy(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	meta, ok := obj.(metav1.Object)
	if !ok {
		runtime.HandleError(errors.New("policy event carried an object without metadata"))
		return
	}
	if key, ok := ownerKey(meta); ok {
		c.queue.Add(v1alpha1.Resource.Resource + "|" + key)
	}
}
```

Add `"fmt"` and `"k8s.io/apimachinery/pkg/runtime/schema"` to the import block.

- [ ] **Step 4: Update 001's three affected tests**

`pkg/controller/controller_test.go` holds three tests that the signature change breaks. They are behavioural tests of 001 and must keep passing; only the key format and the `reconcile` field change.

In `TestProcessNextRequeuesOnError` and `TestProcessNextForgetsOnSuccess`, replace the two lines that build the `Controller`:

```go
	key := queueKey("networkisolations", "isolation-system", "gw-dash")
	q := &fakeQueue{items: []string{key}}
	c := &Controller{queue: q, reconcile: map[string]func(context.Context, string) error{
		"networkisolations": func(context.Context, string) error { return errors.New("apiserver is down") },
	}}
```

and assert on `key` rather than the bare `"isolation-system/gw-dash"`. In `TestProcessNextForgetsOnSuccess` the function returns `nil` instead.

In `TestProcessNextStopsWhenQueueDrains`, the `reconcile` field becomes an empty map:

```go
	c := &Controller{queue: &fakeQueue{}, reconcile: map[string]func(context.Context, string) error{}}
```

In `TestEnqueuePolicy`, the two expected keys gain the resource prefix:

```go
		{"owned policy", owned, []string{"networkisolations|isolation-system/gw-dash"}},
		{"tombstone", cache.DeletedFinalStateUnknown{Key: "tenant-a/netiso-x-0", Obj: owned}, []string{"networkisolations|isolation-system/gw-dash"}},
```

In `TestNewAndRun`, `New` gains the hardening reconciler, and the fake dynamic client must register both list kinds. Replace the construction:

```go
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	hardener := &HardeningReconciler{
		Kube: r.Kube, Dyn: r.Dyn, Protected: r.Protected, Timeout: r.Timeout, Now: r.Now,
	}
	c, err := New(r.Kube, r.Dyn, r, hardener, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
```

and, in `dynClient` in `reconcile_test.go`, add the hardening list kind so the second informer can list:

```go
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			v1alpha1.Resource:          v1alpha1.Kind + "List",
			v1alpha1.HardeningResource: v1alpha1.HardeningKind + "List",
		},
		u,
	)
```

- [ ] **Step 5: Run the whole package**

Run: `go test ./pkg/controller/ -race -cover -v`
Expected: PASS, everything, coverage ≥90%. If `TestNewAndRun` times out waiting for `Active`, the second informer cannot list `workloadhardenings` — the custom list kind in `dynClient` is missing.

- [ ] **Step 6: Wire the binary**

In `cmd/main/main.go`, construct the second reconciler and pass it to `New`. No new flags: `-protected-namespaces`, `-resync` and `-timeout` are shared (BR-05).

```go
	protected := protectedNamespaces(*extra)
	logger.Info("Starting", "protectedNamespaces", slices.Sorted(maps.Keys(protected)), "resync", *resync)

	isolation := &controller.Reconciler{
		Kube:      kube,
		Dyn:       dyn,
		Protected: protected,
		Timeout:   *timeout,
		Now:       time.Now,
	}
	// The same clients, the same protected set and the same timeouts. The
	// hardening reconciler keeps its own dry-run cache and nothing else.
	hardening := &controller.HardeningReconciler{
		Kube:      kube,
		Dyn:       dyn,
		Protected: protected,
		Timeout:   *timeout,
		Now:       time.Now,
	}

	c, err := controller.New(kube, dyn, isolation, hardening, *resync)
	if err != nil {
		logger.Error(err, "Building the controller failed")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	if err := c.Run(ctx); err != nil {
		logger.Error(err, "Controller stopped")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
```

Also update `Run`'s log lines in `controller.go` from `"Starting NetworkIsolation controller"` to `"Starting controller"`, since it now serves two resources.

- [ ] **Step 7: Build and commit**

```bash
go build ./...
go test ./pkg/... -race -cover
go mod vendor
git add cmd pkg vendor
git commit -m "feat(controller): one queue serves both custom resources

Queue keys carry the resource they came from, and New wires a second
informer onto the existing dynamic factory. Hardening shares the binary,
the queue and the worker; it has no workload informer of its own."
```

- [ ] **Step 8: Extend the RBAC**

Append to the `ClusterRole` rules in `deploy/rbac.yaml`, before the `ClusterRoleBinding`:

```yaml
# Workload hardening (002). Target namespaces are chosen at runtime, so this
# cannot be namespace-scoped.
#
# patch, not update: the whole feature is strategic merge patches, and 001's
# lesson is that the two are distinct verbs and only an in-cluster run
# catches a missing one. No create, no delete: this tool never brings a
# workload into existence and never removes one (NFR-03, D-09).
- apiGroups: ["apps"]
  resources: ["deployments", "statefulsets", "daemonsets"]
  verbs: ["get", "list", "patch"]
# Read-only, and only to report them. A ReplicaSet owned by a Deployment is
# skipped because the Deployment is the target; a standalone one is out of
# scope (BR-04, D-08).
- apiGroups: ["apps"]
  resources: ["replicasets"]
  verbs: ["get", "list"]
# Read-only, and only to report them. A Job's template is immutable after
# creation; a CronJob would need a second template path and has no rollout
# net at all (D-08). cronjobs is read because BR-04 requires findings to be
# enumerated whether or not they can be acted on, and a CronJob between
# schedules owns no Job to be reported through.
- apiGroups: ["batch"]
  resources: ["jobs", "cronjobs"]
  verbs: ["get", "list"]
# Read-only. LimitRange decides whether a gap exists at all and whether the
# requested values are admissible. There is deliberately no resourcequotas
# permission: the reachable case is close to unconstructable and any check
# would be an estimate against a moving "used" figure (BR-06, D-15).
- apiGroups: [""]
  resources: ["limitranges"]
  verbs: ["get", "list"]
# No update on the object itself: there is no finalizer to persist, because
# a field written into a workload's own template needs no custodian (FR-05).
- apiGroups: ["hardening.acme.corp"]
  resources: ["workloadhardenings"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["hardening.acme.corp"]
  resources: ["workloadhardenings/status"]
  verbs: ["get", "update", "patch"]
```

`pods` and `namespaces` already carry `get`, `list` and `watch` from 001, which is everything discovery needs.

- [ ] **Step 9: Write the sample workloads**

Create `deploy/samples/workloads-hardening.yaml`. Every Deployment here exists to prove one row of AC-15 against a live cluster, and every container runs the same `busybox` probe so one image covers all of them.

```yaml
apiVersion: v1
kind: Namespace
metadata: { name: harden-a }
---
apiVersion: v1
kind: Namespace
metadata: { name: harden-b }
---
# harden-b's LimitRange sets only `default`, with no `defaultRequest`.
# Kubernetes uses default for defaultRequest when the latter is omitted, so
# every container here already has an effective request and this tool must
# report its gaps as covered rather than filling them (BR-06, AC-06, AC-15).
apiVersion: v1
kind: LimitRange
metadata: { name: default-only, namespace: harden-b }
spec:
  limits:
    - type: Container
      default: { cpu: 200m, memory: 128Mi }
---
# fill-me: no requests, and a container that explicitly runs as root. Root is
# evidenced, so pod-level runAsNonRoot must NOT be written — and the pod must
# stay Ready, which is the whole point (BR-02, AC-05, AC-15).
apiVersion: apps/v1
kind: Deployment
metadata: { name: fill-me, namespace: harden-a }
spec:
  replicas: 2
  selector: { matchLabels: { app: fill-me } }
  template:
    metadata: { labels: { app: fill-me } }
    spec:
      containers:
        - name: probe
          image: busybox:1.37
          command: ["/bin/sh", "-c"]
          args:
            [
              "mkdir -p /srv && hostname > /srv/index.html && httpd -f -p 8080 -h /srv",
            ]
          ports: [{ containerPort: 8080 }]
          securityContext: { runAsUser: 0 }
---
# nonroot: no requests, and a container that runs as a non-root UID. Nothing
# evidences root, so pod-level runAsNonRoot IS written, and the pod must still
# become Ready.
apiVersion: apps/v1
kind: Deployment
metadata: { name: nonroot, namespace: harden-a }
spec:
  replicas: 2
  selector: { matchLabels: { app: nonroot } }
  template:
    metadata: { labels: { app: nonroot } }
    spec:
      containers:
        - name: probe
          image: busybox:1.37
          command: ["/bin/sh", "-c"]
          args:
            [
              "mkdir -p /tmp/srv && hostname > /tmp/srv/index.html && httpd -f -p 8080 -h /tmp/srv",
            ]
          ports: [{ containerPort: 8080 }]
          securityContext: { runAsUser: 65532, runAsGroup: 65532 }
---
# already-hardened: every policy field already present, plus BOTH cpu and
# memory limits and no requests. Its effective requests equal its limits, so it
# has no gap of any kind: no patch, no API call, and QoS stays Guaranteed
# (BR-01, AC-03, AC-15).
apiVersion: apps/v1
kind: Deployment
metadata: { name: already-hardened, namespace: harden-a }
spec:
  replicas: 1
  selector: { matchLabels: { app: already-hardened } }
  template:
    metadata: { labels: { app: already-hardened } }
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: probe
          image: busybox:1.37
          command: ["/bin/sh", "-c"]
          args:
            [
              "mkdir -p /tmp/srv && hostname > /tmp/srv/index.html && httpd -f -p 8080 -h /tmp/srv",
            ]
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: { drop: ["ALL"] }
          resources:
            limits: { cpu: 500m, memory: 1Gi }
---
# skip-me: the escape hatch. Untouched however many gaps it has (BR-04, AC-12).
apiVersion: apps/v1
kind: Deployment
metadata:
  name: skip-me
  namespace: harden-a
  annotations: { hardening.acme.corp/skip: "true" }
spec:
  replicas: 1
  selector: { matchLabels: { app: skip-me } }
  template:
    metadata: { labels: { app: skip-me } }
    spec:
      containers:
        - name: probe
          image: busybox:1.37
          command: ["/bin/sh", "-c"]
          args:
            [
              "mkdir -p /srv && hostname > /srv/index.html && httpd -f -p 8080 -h /srv",
            ]
---
# covered: no requests, in the namespace whose LimitRange supplies them. Its
# securityContext gaps are filled; its resource gaps are reported as covered
# and no request is written (BR-06, AC-06, AC-15).
apiVersion: apps/v1
kind: Deployment
metadata: { name: covered, namespace: harden-b }
spec:
  replicas: 1
  selector: { matchLabels: { app: covered } }
  template:
    metadata: { labels: { app: covered } }
    spec:
      containers:
        - name: probe
          image: busybox:1.37
          command: ["/bin/sh", "-c"]
          args:
            [
              "mkdir -p /tmp/srv && hostname > /tmp/srv/index.html && httpd -f -p 8080 -h /tmp/srv",
            ]
          securityContext: { runAsUser: 65532 }
```

Create `deploy/samples/hardening.yaml`:

```yaml
apiVersion: hardening.acme.corp/v1alpha1
kind: WorkloadHardening
metadata:
  name: tenant-hardening
  namespace: isolation-system
spec:
  namespaces: [harden-a, harden-b]
  resources:
    # Required. Limits are never written (BR-03, D-07).
    requests: { cpu: 10m, memory: 32Mi }
  securityContext:
    # Opt-in, and off by default (BR-02, D-06).
    readOnlyRootFilesystem: false
  # Empty means preview only. Copy hashes out of status.plan[].hash to approve.
  approvedPlan: []
```

- [ ] **Step 10: Add the Makefile targets**

In `Makefile`, add one line to the `deploy` target, immediately after `kubectl apply -f deploy/crd.yaml`:

```make
	kubectl apply -f deploy/crd-hardening.yaml
```

Then append:

```make
samples-hardening:
	kubectl apply -f deploy/samples/workloads-hardening.yaml
	kubectl -n harden-a rollout status deployment/fill-me --timeout=120s
	kubectl -n harden-a rollout status deployment/nonroot --timeout=120s
	kubectl -n harden-a rollout status deployment/already-hardened --timeout=120s
	kubectl -n harden-a rollout status deployment/skip-me --timeout=120s
	kubectl -n harden-b rollout status deployment/covered --timeout=120s

verify-hardening: deploy samples-hardening
	./hack/verify-hardening.sh
```

and add `samples-hardening verify-hardening` to the `.PHONY` line.

- [ ] **Step 11: Write the live verification script (AC-15)**

The only evidence that a patched workload still runs. Fake clients do not run Kubernetes' defaulting, so AC-03's limits-to-requests rule, AC-06's LimitRange rule and AC-05's kubelet behaviour are pinned here and nowhere else (NFR-05).

Create `hack/verify-hardening.sh`:

```bash
#!/usr/bin/env bash
# AC-15, on a live kind cluster: a Deployment with no requests and a root
# container is previewed, approved by hash, applied, rolls out and stays Ready;
# a Deployment declaring both cpu and memory limits and no requests is not
# patched and keeps QoS Guaranteed; a namespace whose LimitRange sets only
# `default` reports its gaps as covered; a skip-annotated Deployment is
# untouched; and the provenance annotation is correct.
set -euo pipefail

request=deploy/samples/hardening.yaml
ns=isolation-system
name=tenant-hardening
fail=0

ok()   { echo "ok    $1"; }
bad()  { echo "FAIL  $1"; fail=1; }

# check <description> <expected> <actual>
check() {
  if [ "$2" = "$3" ]; then ok "$1: $3"; else bad "$1: got '$3', want '$2'"; fi
}

# contains <description> <haystack> <needle>
contains() {
  if printf '%s' "$2" | grep -qF -- "$3"; then ok "$1"; else bad "$1 (missing '$3')"; fi
}

# absent <description> <haystack> <needle>
absent() {
  if printf '%s' "$2" | grep -qF -- "$3"; then bad "$1 (found '$3')"; else ok "$1"; fi
}

filled() { kubectl -n "$1" get deploy "$2" -o jsonpath='{.metadata.annotations.hardening\.acme\.corp/filled}' 2>/dev/null || true; }
tmpl()   { kubectl -n "$1" get deploy "$2" -o jsonpath="{.spec.template.spec$3}" 2>/dev/null || true; }
qos()    { kubectl -n "$1" get pod -l "app=$2" -o jsonpath='{.items[0].status.qosClass}' 2>/dev/null || true; }

echo "== preview =="
kubectl apply -f "$request" >/dev/null
kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=Previewed "workloadhardening/$name" --timeout=90s >/dev/null

plan=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{range .status.plan[*]}{.namespace}/{.kind}/{.name} {.hash}{"\n"}{end}')
findings=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{range .status.findings[*]}{.namespace}/{.name}/{.container}: {.reason}{"\n"}{end}')
echo "$plan"

# Nothing may have been written yet: a preview writes nothing (AC-08).
for d in harden-a/fill-me harden-a/nonroot harden-a/already-hardened harden-a/skip-me harden-b/covered; do
  if [ -n "$(filled "${d%%/*}" "${d##*/}")" ]; then
    bad "preview wrote a provenance annotation onto $d"
  fi
done
ok "preview wrote nothing"

# The plan must name the three targets with gaps and neither of the two without.
contains "fill-me is planned"          "$plan" "harden-a/Deployment/fill-me"
contains "nonroot is planned"          "$plan" "harden-a/Deployment/nonroot"
contains "covered is planned"          "$plan" "harden-b/Deployment/covered"
absent   "already-hardened has no gaps" "$plan" "already-hardened"
absent   "skip-me is not a target"      "$plan" "skip-me"

# BR-01's middle row, and BR-06's, reported rather than acted on.
contains "the defaulted-from-limit finding is reported" "$findings" "defaulted from limit"
contains "the LimitRange coverage finding is reported"  "$findings" "covered by LimitRange"
contains "the skip annotation is reported"              "$findings" "hardening.acme.corp/skip"

echo
echo "== approve by hash =="
hashes=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{range .status.plan[*]}{.hash}{"\n"}{end}' | grep -v '^$')
list=$(printf '%s' "$hashes" | sed 's/.*/"&"/' | paste -sd, -)
kubectl -n "$ns" patch workloadhardening "$name" --type=merge -p "{\"spec\":{\"approvedPlan\":[$list]}}" >/dev/null
kubectl -n "$ns" wait --for=jsonpath='{.status.phase}'=Applied "workloadhardening/$name" --timeout=120s >/dev/null
ok "phase reached Applied"

echo
echo "== the rollout completes and the pods stay Ready =="
kubectl -n harden-a rollout status deployment/fill-me --timeout=180s >/dev/null && ok "fill-me rolled out" || bad "fill-me did not roll out"
kubectl -n harden-a rollout status deployment/nonroot --timeout=180s >/dev/null && ok "nonroot rolled out" || bad "nonroot did not roll out"
kubectl -n harden-b rollout status deployment/covered --timeout=180s >/dev/null && ok "covered rolled out" || bad "covered did not roll out"

echo
echo "== AC-05: root evidence suppressed pod-level runAsNonRoot =="
# fill-me's container declares runAsUser: 0, so writing runAsNonRoot: true
# would produce CreateContainerConfigError and the pods above would never have
# become Ready. This is the assertion the rollout above already proved; making
# it explicit says why.
sc=$(tmpl harden-a fill-me '.securityContext')
absent   "fill-me has no pod-level runAsNonRoot" "$sc" "runAsNonRoot"
contains "fill-me got the seccomp profile"       "$sc" "RuntimeDefault"
csc=$(tmpl harden-a fill-me '.containers[0].securityContext')
contains "fill-me got allowPrivilegeEscalation"  "$csc" "allowPrivilegeEscalation"
contains "fill-me got capabilities.drop"         "$csc" "ALL"

# nonroot evidences nothing, so it does get the field, and it is still Ready.
contains "nonroot got pod-level runAsNonRoot" "$(tmpl harden-a nonroot '.securityContext')" "runAsNonRoot"

echo
echo "== AC-03: a limits-only Deployment is untouched and stays Guaranteed =="
check "already-hardened has no provenance annotation" "" "$(filled harden-a already-hardened)"
absent "already-hardened has no requests written" "$(tmpl harden-a already-hardened '.containers[0].resources')" "requests"
check "already-hardened QoS" "Guaranteed" "$(qos harden-a already-hardened)"

echo
echo "== AC-06: a LimitRange with only 'default' covers the requests =="
check "covered was patched" "true" "$([ -n "$(filled harden-b covered)" ] && echo true || echo false)"
absent "covered got no resource requests" "$(filled harden-b covered)" "resources.requests"
contains "covered got its securityContext" "$(filled harden-b covered)" "allowPrivilegeEscalation"

echo
echo "== AC-12: the skip annotation is honoured =="
check "skip-me has no provenance annotation" "" "$(filled harden-a skip-me)"
check "skip-me has no pod securityContext" "" "$(tmpl harden-a skip-me '.securityContext')"

echo
echo "== AC-11: the provenance annotation records leaf paths and values =="
prov=$(filled harden-a fill-me)
printf '%s\n' "$prov"
for want in \
  "spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault" \
  "spec.template.spec.containers[probe].securityContext.allowPrivilegeEscalation=false" \
  "spec.template.spec.containers[probe].securityContext.capabilities.drop=[ALL]" \
  "spec.template.spec.containers[probe].resources.requests.cpu=10m" \
  "spec.template.spec.containers[probe].resources.requests.memory=32Mi"; do
  contains "provenance records $want" "$prov" "$want"
done
absent "provenance never records runAsNonRoot for fill-me" "$prov" "runAsNonRoot"
absent "provenance never records a limit"                  "$prov" "limits"

echo
echo "== the applied object is terminal =="
before=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{.status.lastReconcileTime}')
sleep 35   # longer than the controller's 30s resync
after=$(kubectl -n "$ns" get workloadhardening "$name" -o jsonpath='{.status.lastReconcileTime}')
check "an Applied object is not rewritten on resync" "$before" "$after"

echo
if [ "$fail" -ne 0 ]; then
  echo "AC-15 FAILED"
  exit 1
fi
echo "AC-15 PASSED"
```

- [ ] **Step 12: Run it**

```bash
chmod +x hack/verify-hardening.sh
make verify-hardening
```

Expected: every line `ok`, final `AC-15 PASSED`.

Three things that go wrong here, and what they mean:

- **`fill-me did not roll out`, and its pods show `CreateContainerConfigError: container has runAsNonRoot and image will run as root`.** This is the failure the whole feature is built to avoid: pod-level `runAsNonRoot` was written beside a container declaring `runAsUser: 0`. The bug is in `buildPod`, not in the sample. Task 3, Step 9 should have caught it — go back and check that `rootEvidence` runs before the field is decided.
- **`already-hardened QoS: got 'Burstable', want 'Guaranteed'`.** The limits-only container was treated as having a request gap and was patched, cutting its reservation. That is AC-03's exact harm, performed by the tool. The bug is in `buildResources`'s `hasLimit` branch.
- **`covered got no resource requests` fails, i.e. requests were written.** `Cover` read `defaultRequest` only and missed that `default` supplies it. That restarts every pod in `harden-b` to write a value the API server was already injecting.

- [ ] **Step 13: Record the versions actually used**

```bash
kind version; kubectl version --client; go version
kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion}'; echo
```

Keep the output for the README.

- [ ] **Step 14: Extend the README**

`README.md` already covers core task 1. Add a core task 2 section covering, because the assignment and NFR-06 ask for each: what it does, the setup commands, the decisions taken, and the limitations.

```markdown
## Core task 2 — on-demand workload hardening

Creating a `WorkloadHardening` object **previews** what would change across the
namespaces it names. Nothing is written until the operator copies the hashes
they accept into `spec.approvedPlan`.

    kubectl apply -f deploy/samples/hardening.yaml
    kubectl -n isolation-system get wh tenant-hardening -o jsonpath='{.status.plan[*].hash}'
    kubectl -n isolation-system patch wh tenant-hardening --type=merge \
      -p '{"spec":{"approvedPlan":["<hash>","<hash>"]}}'

    make samples-hardening      # harden-a and harden-b, five Deployments, one LimitRange
    make verify-hardening       # AC-15: preview, approve, apply, rollout, QoS, provenance
    make verify-crd-hardening   # AC-16: API-server schema and per-field immutability

### What it fills

| Field                      | Level     | Value            | Written                  |
| -------------------------- | --------- | ---------------- | ------------------------ |
| `runAsNonRoot`             | pod       | `true`           | unless root is evidenced |
| `seccompProfile.type`      | pod       | `RuntimeDefault` | always                   |
| `allowPrivilegeEscalation` | container | `false`          | always                   |
| `capabilities.drop`        | container | `["ALL"]`        | always                   |
| `readOnlyRootFilesystem`   | container | `true`           | only when requested      |

plus absent **resource requests**, from the values in the object. Limits are
never written.

### Decisions

- **Gaps are judged on the effective value, not on what the template says.**
  Kubernetes copies `limits` into `requests` when requests are absent, and it
  does so when defaulting the **Pod** — never the workload template. A
  Deployment declaring `limits: {cpu: 500m, memory: 1Gi}` and no requests runs
  as QoS `Guaranteed` with `requests == limits`. A tool that reads templates
  sees an absent field and calls it a gap; filling it would cut the CPU
  reservation 50×, the memory reservation 32×, and demote the pod to
  `Burstable`. So a `limits`-only container is a **finding**, never a gap — and
  the same rule applies to a namespace LimitRange, whose `default` supplies the
  request when `defaultRequest` is omitted.
- **`runAsNonRoot` is written at pod level, so one container needing root
  suppresses it for the whole pod.** Evidence is an explicit `privileged: true`
  or an effective `runAsUser: 0`. Reporting such a container as a finding while
  still writing the field is the most likely way a tool like this takes out a
  DaemonSet: CNI agents, log shippers and node exporters are routinely
  privileged without ever declaring `runAsUser: 0`, and no dry-run refuses it —
  the failure is the kubelet's.
- **Approval is per target, by hash.** A plan-wide hash cannot converge: any CI
  deploy touching any workload in any of the named namespaces moves it. Per
  target also lets an operator approve a subset deliberately, which is the
  normal way to use a gate like this.
- **No finalizer, no workload informer, no drift repair.** The tool does not own
  the fields it writes. A reconcile loop would eventually overwrite a deliberate
  later change — someone raising a memory limit after an OOMKill — and restart
  pods to do it. Deleting the request leaves the patches in place, so no object
  is ever stuck waiting on this controller.
- **Provenance lives on the target, not only in status.** Status dies with the
  custom resource, and an operator inspecting a workload should be able to see
  what changed it without knowing this tool exists.
- **Limits are never written.** The tool cannot know a workload's working set,
  and one number spread across sixteen namespaces is guaranteed wrong for some
  of them. A memory limit that is too small kills the container after a rollout
  that completed green. LimitRange is the per-namespace mechanism that exists
  for this, and the tool routes operators to it.

### Limitations

- **The dry-run is not a safety net for securityContext.** It catches schema,
  admission and webhook problems. Every root-related failure is enforced by the
  kubelet or the kernel and passes a dry-run cleanly.
- **`Applied` means the API server accepted every approved patch.** It does not
  assert that the resulting pods became Ready. A halted rollout is visible in
  the workload but not in this object's status.
- **A patched pod that becomes Ready and fails later is not detected** — a
  denied syscall, a missing capability, a setuid exec. Those three are the
  restricted PSS baseline and are the feature. `readOnlyRootFilesystem` is
  opt-in precisely because its failure is reliably late.
- **The tool cannot harden a workload that is unhardened by explicit choice.**
  It reports it instead; it never overrules a decision someone made on purpose.
- **A template patch restarts every pod of every target.** The preview reports
  the pod count and the rollout mechanism per target; the tool does not stage,
  throttle or canary.
- **There is no undo in this version.** The provenance annotation is the
  checkpoint one would work from.
- **Two requests may name the same namespace**, and the second one's provenance
  annotation replaces the first's.
```

Fill every `<...>` placeholder with real content before committing — leaving one in is a failed deliverable, since NFR-06 requires the recorded versions. Update the existing "Tested versions" section with the Step 13 output if anything moved.

- [ ] **Step 15: Run everything one last time**

```bash
make test cover
make verify-crd
make verify-crd-hardening
make verify
make verify-hardening
```

Expected: unit tests PASS with coverage ≥90% per package, `AC-09 PASSED`, `AC-16 PASSED`, `AC-07 PASSED`, `AC-15 PASSED`. 001's two scripts must still pass — the queue change is the only thing that touched it.

- [ ] **Step 16: Commit**

```bash
git add deploy hack Makefile README.md
git commit -m "feat(deploy): RBAC, samples, live verification and README for hardening

Covers AC-15 on a live cluster, which is the only evidence for the rules a
fake client cannot exercise: limits-to-requests defaulting, LimitRange
coverage, and the kubelet's enforcement of runAsNonRoot.

The ClusterRole grants patch and not update on workloads, no create and no
delete, and no resourcequotas at all. batch/cronjobs is read-only and exists
only so a CronJob between schedules is still reported (BR-04, D-08)."
```

---

## Acceptance criteria coverage

| ID    | Covered by                                                                                                       |
| ----- | ---------------------------------------------------------------------------------------------------------------- |
| AC-01 | Task 3, Step 1 — `TestBuildAlwaysOnFields`; Task 5, Step 8 — `TestPatchBody`                                     |
| AC-02 | Task 3, Step 6 — `TestSecurityContextPrecedence`                                                                 |
| AC-03 | Task 4, Step 1 — `TestEffectiveRequests`; Task 9 — `hack/verify-hardening.sh` (QoS `Guaranteed`)                 |
| AC-04 | Task 3, Step 1 — `TestReadOnlyRootFilesystemIsOptIn`; Task 5, Step 8 — `TestPatchBody` (no `limits` key)         |
| AC-05 | Task 3, Step 8 — `TestRootEvidenceSuppressesRunAsNonRoot`; Task 9 — `hack/verify-hardening.sh` (pods stay Ready) |
| AC-06 | Task 4, Steps 6 and 8 — `TestLimitRangeCoverage`, `TestSeveralLimitRanges`; Task 9 — `hack/verify-hardening.sh`  |
| AC-07 | Task 6, Step 1 — `TestDiscoverRefusesConfigurationsThatDoNotRollOut`                                             |
| AC-08 | Task 7, Step 1 — `TestPreviewWritesNothing`                                                                      |
| AC-09 | Task 8, Step 1 — `TestPerTargetApproval`, `TestStaleApprovalHoldsPartiallyApplied`                               |
| AC-10 | Task 7, Step 7 — `TestUnarmedResyncSkipsUnchangedTargets`; Task 8, Step 1 — `TestApplyAlwaysDryRunsFirst`        |
| AC-11 | Task 5, Step 8 — `TestPatchBody`; Task 8, Step 1 — `TestProvenanceRidesInTheSameRequest`                         |
| AC-12 | Task 6, Step 1 — `TestDiscoverExcludesAndReports`; Task 7, Step 1 — `TestValidationIsAllOrNothing`               |
| AC-13 | Task 8, Step 6 — `TestPartialFailureKeepsWhatSucceededAndConverges`                                              |
| AC-14 | Task 8, Step 6 — `TestTerminalAndRecoveringPhases`                                                               |
| AC-15 | Task 9 — `hack/verify-hardening.sh`                                                                              |
| AC-16 | Task 2 — `hack/verify-crd-hardening.sh`                                                                          |
| AC-17 | Task 8, Steps 9-10 — `TestApprovalMatchingNothingIsNotTerminal`, `TestDanglingApprovalOutlivesASuccessfulPatch`  |
| AC-18 | Task 8, Steps 9-10 — `TestApprovalExtendedAfterApplied`                                                          |

## Review Focus coverage

| #   | Condition                                              | Covered by                                                          |
| --- | ------------------------------------------------------ | ------------------------------------------------------------------- |
| 1   | A change hash that moves between passes                | Task 5, Step 6 — `TestHashIsStableAcrossPasses`                     |
| 2   | Several LimitRanges, or several Container-scoped items | Task 4, Step 8 — `TestSeveralLimitRanges`                           |
| 3   | An `approvedPlan` matching nothing                     | Task 8, Step 9 — `TestApprovalMatchingNothingIsNotTerminal`         |
| 4   | A target that is already fully hardened                | Task 7, Step 9 — `TestFullyHardenedTargetIssuesNoAPICall`           |
| 5   | A namespace that vanishes between validation and apply | Task 8, Step 11 — `TestNamespaceVanishingBetweenValidationAndApply` |
| 6   | A stored object whose spec will not convert            | Task 7, Step 11 — `TestUnreadableSpecIsRejectedNotRetriedForever`   |

## Deviations and clarifications to confirm before merging

Per `AGENTS.md`, the Software Engineer may not invent requirements. Each item below is a point where the spec is silent or admits two readings; the plan states which reading it takes and why, rather than resolving it quietly.

Items 1-6, 8 and 9 have since been **settled in the spec itself** — FR-01, FR-02, FR-03, FR-05, FR-06, BR-07, NFR-03, AC-05, G-05 and the new AC-17/AC-18 — rather than worked around here. `AGENTS.md` reserves `/specs` for the Spec Architect; those edits were made with the user's explicit consent, as `2b86936` was in 001. Item 7 stands as a reading of an acceptance criterion that needs no spec text, and items 10-12 are facts about the tooling rather than open questions. `AGENTS.md` reserves `/specs` for the Spec Architect; those edits were made with the user's explicit consent and are flagged as such, the same way `2b86936` was in 001. The remaining items are still open questions for review.

1. **RESOLVED IN SPEC — `plan.Build` returns findings as well as changes.** FR-02 gave the signature as `plan.Build(template, policy) → []Change`, but BR-04 requires findings enumerated and nothing else in the spec said what produces them. FR-02 now reads `→ Plan`, a struct carrying both lists, matching `func Build(pod *corev1.PodSpec, policy Policy) Plan` in Task 3, and records why they come from one traversal: deciding that a container's effective request is defaulted from its limit is itself what produces the finding, so a second pass would re-derive every effective value the first had already computed. No behaviour differs; only the return type.

2. **RESOLVED IN SPEC — `Planned` as the preview outcome.** FR-06 listed the per-target outcomes as `Patched`, `Failed`, `Stale`, `Unapproved`, "or a finding reason" — none of which describes a target on an unarmed object, where nothing has been executed and there is no approval for `Stale` or `Unapproved` to be about. That left the most common row in the feature — a healthy target awaiting approval — with no legal value, and `Outcome` carries no `omitempty`, so it would have serialised as an empty string and read as a fault. FR-06 now names `Planned`. The CRD declares `outcome` as a free string rather than an enumeration, so this cost vocabulary and no schema change.

3. **RESOLVED IN SPEC — how `Stale` is told from `Unapproved`.** BR-07 defined both by outcome ("changed since approval" versus "appeared after approval") but not by mechanism, and the controller holds only `spec.approvedPlan` — a flat list of hashes with no target attached, so neither claim about the past is decidable from `spec` alone. BR-07 now specifies the mechanism: resolve both against the plan last published in `status.plan`, and report `Unapproved` where no such record exists. The degradation is stated there and is bounded to the row's *label* — the phase turns on FR-06's unmatched-hash rule, which needs no history. BR-07 also records why the two can never disagree: the canonical form is prefixed with the target's namespace, kind and name, so a `Stale` target's previously approved hash describes no target at all and is necessarily among the unmatched hashes FR-06 already holds the object open for.

4. **RESOLVED IN SPEC — an approved hash that matches no target holds the object non-terminal, whether or not other targets patched.** FR-06 defines `Applied` as "every approved target was patched" and `PartiallyApplied` as "at least one approved target failed or is `Stale`". Where zero targets patched, zero failed and zero stale, neither definition fits, and the literal reading of `Applied` — vacuously true — is **terminal**, stranding the operator's approval having done nothing, forever. The mixed case is worse and less obvious: one approval lands, another matches nothing, `patched > 0` reads as `Applied`, and the second approval is never revisited. The plan therefore folds the phase on the count of **dangling approvals** rather than on `Stale` rows, which covers both and, unlike a `Stale` count, needs no status history. `PartiallyApplied` with a message naming the count. This is Review Focus 3, tested at Task 8 Steps 9 and 10.

   **FR-06 and FR-05 have been amended rather than worked around here.** `Applied` now requires every hash in `approvedPlan` to have matched a target, `PartiallyApplied` names the unmatched case, and FR-05 gates terminality on `status.observedGeneration` so a corrected or extended approval is acted on — which also repairs BR-07's subset approval and FR-01's batching, both of which the old unconditional terminality silently killed. `observedGeneration` moves out of G-05 into this version, and AC-17 and AC-18 cover the two behaviours. AC-14 is unchanged: a resync does not move `generation`.

5. **RESOLVED IN SPEC — `status.plan` retains `Patched` rows across passes.** Previously unstated; FR-06 now says so. It follows from BR-01: an already-patched target has no gaps left, so it drops out of the recomputed plan entirely, and an `Applied` object whose plan rendered empty would tell an operator nothing about what the tool did. It is also what lets the phase fold tell item 4's case from a successful apply.

6. **RESOLVED IN SPEC — a privileged container receives no securityContext change at all.** AC-05 now scopes "the other three fields are still written" to the pod and its *other* containers explicitly. BR-02 says "dropping its capabilities would be theatre" and BR-04 lists privileged containers under "reported as findings, never patched", but AC-05 says "the other three fields are still written". The plan reads AC-05 as scoped to the pod and its other containers, and exempts the privileged container itself — because the API server rejects `allowPrivilegeEscalation: false` alongside `privileged: true` outright, so writing it would fail the whole target's dry-run and take its sibling containers down with it. If AC-05 means the privileged container should receive those fields, the feature cannot patch any pod containing one.

   **Verified on kind (v1.36.2), server-side dry-run against a Deployment:**

   ```console
   $ kubectl apply --dry-run=server -f privileged-plus-noescalate.yaml
   The Deployment "probe-a" is invalid: spec.template.spec.containers[0].securityContext:
   Invalid value: {...}: cannot set `allowPrivilegeEscalation` to false and `privileged` to true

   $ kubectl apply --dry-run=server -f privileged-plus-dropall.yaml
   deployment.apps/probe-b created (server dry run)
   ```

   So the two exemptions rest on different grounds, and the plan states them separately: `allowPrivilegeEscalation` is **refused by the API server** and cannot be written at any cost, while `capabilities.drop` is accepted and is omitted purely on BR-02's "theatre" judgment. This is object-level validation, so the dry-run of FR-03 does catch it — the cost of getting it wrong is a whole target's patch failing, not a late kubelet failure.

7. **`AC-14`'s "no API calls" is read as "no cluster calls and no writes".** Reading the `WorkloadHardening` object itself is unavoidable — the controller cannot know the phase is `Applied` without it. The test asserts zero calls against the workload clients and zero writes to the custom resource. 001 draws the same line with `assertNoWrites`.

8. **RESOLVED IN SPEC — a dry-run refusal during a preview does not requeue.** The error table said only that the refusal "is reported as that target's outcome", and FR-06 has no phase for a partly refused preview. FR-03 now states it: the object stays `Previewed`, the row carries the refusal, and the resync retries — returning an error would spin the queue's backoff against a webhook that may refuse permanently, on behalf of an object nobody armed. During an **apply** the same refusal is a `Failed` target and does return an error, per FR-04.

9. **RESOLVED IN SPEC — CronJobs are reported in their own right, and NFR-03 now grants `cronjobs` get/list.** D-08 requires CronJobs reported; NFR-03 previously enumerated `pods`, `jobs`, `replicasets`, `limitranges` and `namespaces` without naming `cronjobs`. Reporting them through the Jobs they own would stay inside that enumeration, but **a CronJob between schedules owns no Job and would be missing from the report entirely** — and BR-04 is explicit that findings are enumerated whether or not they can be acted on, "because a namespace reported as hardened while a root pod runs in it is a lie". NFR-03 reads as the access the feature needs rather than a closed list: what it actually forbids is `resourcequotas`, `delete` on any workload, pod exec and secrets, none of which this touches. One read-only verb pair is the cheaper error than a silently incomplete report. A Job owned by a CronJob still names its owner, so the two findings tie together.

10. **RESOLVED — 001's plan document contradicted the shipped API group.** `specs/001-network-isolation/tasks.md` said `hardening.k8s.io` in its Global Constraints and several code blocks while the CRD it shipped said `hardening.acme.corp`. Cause: `350030b` (the `spec.peers` rewrite) was authored against the pre-`2b86936` document and reverted the group fix. Both incomplete syncs have been finished — the group string, the stale `verify-crd.sh` block and the `isolation.yaml` sample now match the shipped files byte for byte — and that document carries a Status note listing the three later fixes (`4c555c9`, `ec35cba`, `bb62284`) whose code it still does not reflect. Nothing in 002 depends on this beyond using `hardening.acme.corp`, which is what its spec requires.

11. **`fake.Clientset` does not honour `DryRun`.** Verified against `k8s.io/client-go@v0.37.1`: a `Patch` carrying `DryRun: [All]` mutates the fake's object tracker. Every test asserting that a preview wrote nothing therefore installs the `dryRunGuard` reactor from Task 7, Step 1. Without it those assertions pass vacuously, and the feature's central safety property would be untested. This is a property of the test double, not of the spec, but it is the single most load-bearing detail in Tasks 7 and 8.

12. **Neither fake bumps `metadata.generation`.** The API server moves it on every write that changes `spec`; `dynamic/fake` does not. FR-05's terminality gate compares it against `status.observedGeneration`, so a test that armed an object without moving it could never exercise a re-approval. The `arm` helper increments it explicitly, with a comment saying why. Like item 11 this is a property of the test double, not of the spec, and it is what AC-18 rests on.
