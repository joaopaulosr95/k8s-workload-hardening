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

// A stored object whose spec will not convert must produce an error and no
// object, so the reconciler can report it rather than retry forever
// (Review Focus 6, whose controller half is Task 7).
func TestHardeningFromUnstructuredRejectsMalformedObject(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": GroupName + "/" + Version,
		"kind":       HardeningKind,
		"metadata":   map[string]any{"name": "bad", "namespace": "isolation-system"},
		"spec": map[string]any{
			// namespaces holds strings; an int64 cannot be converted.
			"namespaces": []any{int64(42)},
		},
	}}

	w, err := HardeningFromUnstructured(in)
	if err == nil {
		t.Fatalf("want an error, got %+v", w)
	}
	if w != nil {
		t.Errorf("want a nil object alongside the error, got %+v", w)
	}
}
