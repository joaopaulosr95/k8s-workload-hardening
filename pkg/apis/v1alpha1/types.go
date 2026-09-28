// Package v1alpha1 holds the NetworkIsolation API types. They are plain structs
// converted to and from unstructured objects, so the project needs no generated
// clientset and no deepcopy functions.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "hardening.acme.corp"
	Version   = "v1alpha1"
	Kind      = "NetworkIsolation"

	// Finalizer is persisted before the first policy write, so cleanup is
	// guaranteed a chance to run (FR-03).
	Finalizer = "hardening.acme.corp/cleanup"
	// OperationLabel carries the owning object's UID on every generated policy.
	// Only policies bearing it are ever updated or deleted.
	OperationLabel = "hardening.acme.corp/operation"
	// OwnerAnnotation records "<namespace>/<name>" of the owning object, so a
	// policy event can be mapped back to the object without a lookup table.
	OwnerAnnotation = "hardening.acme.corp/owner"
)

// Resource is the GVR the dynamic client uses for NetworkIsolation objects.
var Resource = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "networkisolations"}

// GroupVersionKind stamps unstructured objects on the way out.
var GroupVersionKind = schema.GroupVersionKind{Group: GroupName, Version: Version, Kind: Kind}

// Phase is the coarse state reported in status (FR-05).
type Phase string

const (
	PhasePending  Phase = "Pending"
	PhaseRejected Phase = "Rejected"
	PhaseActive   Phase = "Active"
	PhaseDegraded Phase = "Degraded"
	PhaseDeleting Phase = "Deleting"
)

// Group is one of the two pod sets being isolated. Membership is whatever the
// selector matches right now; the controller never reads workload objects.
type Group struct {
	Namespace   string               `json:"namespace"`
	PodSelector metav1.LabelSelector `json:"podSelector"`
}

// Spec is immutable once created, enforced by a CEL rule in the CRD (FR-01).
type Spec struct {
	A Group `json:"a"`
	B Group `json:"b"`
}

// Status reports what the controller observed. MatchedA and MatchedB carry no
// omitempty: zero is an observation and is reported explicitly (FR-05).
type Status struct {
	Phase             Phase    `json:"phase,omitempty"`
	Message           string   `json:"message,omitempty"`
	Policies          []string `json:"policies,omitempty"`
	MatchedA          int      `json:"matchedA"`
	MatchedB          int      `json:"matchedB"`
	LastReconcileTime string   `json:"lastReconcileTime,omitempty"`
}

type NetworkIsolation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Spec   `json:"spec"`
	Status            Status `json:"status,omitempty"`
}

// FromUnstructured converts an object read through the dynamic client.
func FromUnstructured(u *unstructured.Unstructured) (*NetworkIsolation, error) {
	iso := &NetworkIsolation{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, iso); err != nil {
		return nil, err
	}
	return iso, nil
}

// ToUnstructured converts back for a write through the dynamic client.
func ToUnstructured(iso *NetworkIsolation) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(iso)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(GroupVersionKind)
	return u, nil
}
