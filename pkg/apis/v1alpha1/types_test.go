package v1alpha1

import (
	"reflect"
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
			"peers": []any{
				map[string]any{
					"namespace":   "tenant-a",
					"podSelector": map[string]any{"matchLabels": map[string]any{"app": "gateway"}},
				},
				map[string]any{
					"namespace":   "tenant-b",
					"podSelector": map[string]any{"matchLabels": map[string]any{"app": "dashboard"}},
				},
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
	if len(iso.Spec.Peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(iso.Spec.Peers))
	}
	if iso.Spec.Peers[0].Namespace != "tenant-a" || iso.Spec.Peers[1].PodSelector.MatchLabels["app"] != "dashboard" {
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
	iso := &NetworkIsolation{Status: Status{
		Phase: PhaseActive,
		Peers: []PeerStatus{{Policy: "netiso-x-0", Matched: 0}, {Policy: "netiso-x-1", Matched: 0}},
	}}
	out, err := ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	status, ok := out.Object["status"].(map[string]any)
	if !ok {
		t.Fatal("status missing")
	}
	peers, ok := status["peers"].([]any)
	if !ok || len(peers) != 2 {
		t.Fatalf("status.peers = %v", status["peers"])
	}
	for i, raw := range peers {
		peer := raw.(map[string]any)
		if _, ok := peer["matched"]; !ok {
			t.Errorf("peer %d: matched dropped when zero", i)
		}
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
			"peers": []any{map[string]any{"namespace": int64(42)}}, // namespace is a string
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
		Status: Status{Phase: PhaseActive, Message: "both policies written"},
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
