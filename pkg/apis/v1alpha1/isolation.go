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
	GroupName     = "hardening.acme.corp"
	Version       = "v1alpha1"
	IsolationKind = "NetworkIsolation"

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

// IsolationResource is the GVR the dynamic client uses for NetworkIsolation
// objects.
var IsolationResource = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "networkisolations"}

// IsolationGroupVersionKind stamps unstructured objects on the way out.
var IsolationGroupVersionKind = schema.GroupVersionKind{Group: GroupName, Version: Version, Kind: IsolationKind}

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
// Peers always holds exactly two entries: the CRD pins the length, because one
// pair needs no policy compiler and several pairs are separate objects (D-01).
// The two are symmetric — the block is mutual, and their order carries no
// meaning beyond indexing the generated policies.
type IsolationSpec struct {
	Peers []Group `json:"peers"`
}

// PeerStatus is what the controller observed about one peer. Matched carries no
// omitempty: zero is an observation and is reported explicitly (FR-05).
type PeerStatus struct {
	Policy  string `json:"policy"`
	Matched int    `json:"matched"`
}

// Status reports what the controller observed. Peers is index-aligned with
// Spec.Peers.
type IsolationStatus struct {
	Phase             Phase        `json:"phase,omitempty"`
	Message           string       `json:"message,omitempty"`
	Peers             []PeerStatus `json:"peers,omitempty"`
	LastReconcileTime string       `json:"lastReconcileTime,omitempty"`
}

type NetworkIsolation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              IsolationSpec   `json:"spec"`
	Status            IsolationStatus `json:"status,omitempty"`
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
	u.SetGroupVersionKind(IsolationGroupVersionKind)
	return u, nil
}
