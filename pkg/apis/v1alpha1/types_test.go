package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A dynamic-client object must survive the trip into the typed struct and back
// with the fields the reconciler depends on intact: UID, finalizers, the
// deletion timestamp and both selectors.
func TestUnstructuredRoundTrip(t *testing.T) {
	now := metav1.Now()
	in := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": GroupName + "/" + Version,
		"kind":       Kind,
		"metadata": map[string]any{
			"name":              "gateway-dashboard",
			"namespace":         "isolation-system",
			"uid":               "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8",
			"finalizers":        []any{Finalizer},
			"deletionTimestamp": now.UTC().Format("2006-01-02T15:04:05Z"),
		},
		"spec": map[string]any{
			"a": map[string]any{
				"namespace":   "tenant-a",
				"podSelector": map[string]any{"matchLabels": map[string]any{"app": "gateway"}},
			},
			"b": map[string]any{
				"namespace":   "tenant-b",
				"podSelector": map[string]any{"matchLabels": map[string]any{"app": "dashboard"}},
			},
		},
	}}

	iso, err := FromUnstructured(in)
	if err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if string(iso.UID) != "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Errorf("UID = %q", iso.UID)
	}
	if len(iso.Finalizers) != 1 || iso.Finalizers[0] != Finalizer {
		t.Errorf("Finalizers = %v", iso.Finalizers)
	}
	if iso.DeletionTimestamp == nil {
		t.Error("DeletionTimestamp lost in conversion")
	}
	if iso.Spec.A.Namespace != "tenant-a" || iso.Spec.B.PodSelector.MatchLabels["app"] != "dashboard" {
		t.Errorf("Spec = %+v", iso.Spec)
	}

	out, err := ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	if out.GetKind() != Kind || out.GetAPIVersion() != GroupName+"/"+Version {
		t.Errorf("GVK = %s %s", out.GetAPIVersion(), out.GetKind())
	}
	if out.GetUID() != iso.UID {
		t.Errorf("UID = %q", out.GetUID())
	}
}

// Zero counts are part of the report, not an absence (FR-05), so they must not
// be dropped by omitempty on the way out.
func TestZeroCountsAreSerialised(t *testing.T) {
	iso := &NetworkIsolation{Status: Status{Phase: PhaseActive, MatchedA: 0, MatchedB: 0}}
	out, err := ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	status, ok := out.Object["status"].(map[string]any)
	if !ok {
		t.Fatal("status missing")
	}
	if _, ok := status["matchedA"]; !ok {
		t.Error("matchedA dropped when zero")
	}
	if _, ok := status["matchedB"]; !ok {
		t.Error("matchedB dropped when zero")
	}
}

// A stored object whose field types do not match the schema must surface as an
// error rather than a silently half-populated struct: the reconciler would
// otherwise write policies from a spec it never really read.
func TestFromUnstructuredRejectsMalformedObject(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": GroupName + "/" + Version,
		"kind":       Kind,
		"metadata":   map[string]any{"name": "bad", "namespace": "isolation-system"},
		"spec": map[string]any{
			"a": map[string]any{"namespace": int64(42)}, // namespace is a string
		},
	}}

	iso, err := FromUnstructured(in)
	if err == nil {
		t.Fatalf("want an error, got %+v", iso)
	}
	if iso != nil {
		t.Errorf("want a nil object alongside the error, got %+v", iso)
	}
}
