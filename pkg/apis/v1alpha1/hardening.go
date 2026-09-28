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
