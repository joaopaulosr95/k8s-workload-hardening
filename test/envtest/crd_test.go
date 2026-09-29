package envtest_test

import (
	"fmt"
	"testing"
)

// peer is one entry of NetworkIsolation's two-element peers list.
func peer(namespace string, matchLabels map[string]any) map[string]any {
	return map[string]any{
		"namespace":   namespace,
		"podSelector": map[string]any{"matchLabels": matchLabels},
	}
}

// requests is the resources block every WorkloadHardening case needs.
func requests(cpu, memory string) map[string]any {
	return map[string]any{"requests": map[string]any{"cpu": cpu, "memory": memory}}
}

// TestIsolationSchema ports the six create-time rejections from
// hack/verify-crd-isolation.sh. The script is the record of what this schema
// is supposed to refuse, so these are ported rather than invented.
func TestIsolationSchema(t *testing.T) {
	for i, tc := range []struct {
		name, want string
		spec       map[string]any
	}{
		{"only one peer supplied", "should have at least 2 items",
			map[string]any{"peers": []any{peer("tenant-a", map[string]any{"app": "gateway"})}}},
		{"three peers supplied", "Too many: 3: must have at most 2 items",
			map[string]any{"peers": []any{
				peer("tenant-a", map[string]any{"app": "gateway"}),
				peer("tenant-b", map[string]any{"app": "dashboard"}),
				peer("tenant-c", map[string]any{"app": "extra"}),
			}}},
		{"peers omitted entirely", "spec.peers: Required value",
			map[string]any{}},
		{"matchExpressions supplied", "matchExpressions: Too many",
			map[string]any{"peers": []any{
				map[string]any{"namespace": "tenant-a", "podSelector": map[string]any{
					"matchLabels":      map[string]any{"app": "gateway"},
					"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"gateway"}}},
				}},
				peer("tenant-b", map[string]any{"app": "dashboard"}),
			}}},
		{"empty matchLabels", "should have at least 1 properties",
			map[string]any{"peers": []any{
				peer("tenant-a", map[string]any{}),
				peer("tenant-b", map[string]any{"app": "dashboard"}),
			}}},
		{"invalid namespace name", "spec.peers[0].namespace in body should match",
			map[string]any{"peers": []any{
				peer("Tenant_A", map[string]any{"app": "gateway"}),
				peer("tenant-b", map[string]any{"app": "dashboard"}),
			}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejects(t, isolationGVR, tc.want, object("NetworkIsolation", fmt.Sprintf("iso-%02d", i), tc.spec))
		})
	}
}

// TestIsolationTransitionRule ports the script's three sequenced cases. One
// test rather than subtests: spec immutability only exists on an update, so
// the order is the assertion. A prefixed label key is legal and must survive.
func TestIsolationTransitionRule(t *testing.T) {
	valid := func(secondLabels map[string]any) map[string]any {
		return object("NetworkIsolation", "iso-valid", map[string]any{"peers": []any{
			peer("tenant-a", map[string]any{"app": "gateway"}),
			peer("tenant-b", secondLabels),
		}})
	}
	prefixed := map[string]any{"example.com/tier": "gold"}

	accepts(t, isolationGVR, valid(prefixed))
	rejects(t, isolationGVR, "spec is immutable", valid(map[string]any{"app": "retargeted"}))
	// Re-applying the identical spec must still be allowed: self == oldSelf holds.
	accepts(t, isolationGVR, valid(prefixed))
}

// TestHardeningSchema ports the ten create-time rejections from
// hack/verify-crd-hardening.sh.
func TestHardeningSchema(t *testing.T) {
	for i, tc := range []struct {
		name, want string
		spec       map[string]any
	}{
		{"empty namespace list", "should have at least 1 items",
			map[string]any{"namespaces": []any{}, "resources": requests("10m", "32Mi")}},
		{"seventeen namespaces", "must have at most 16 items",
			map[string]any{"namespaces": []any{"n01", "n02", "n03", "n04", "n05", "n06", "n07", "n08", "n09", "n10", "n11", "n12", "n13", "n14", "n15", "n16", "n17"}, "resources": requests("10m", "32Mi")}},
		{"duplicate namespace", "Duplicate value",
			map[string]any{"namespaces": []any{"tenant-a", "tenant-a"}, "resources": requests("10m", "32Mi")}},
		{"resources omitted entirely", "spec.resources: Required value",
			map[string]any{"namespaces": []any{"tenant-a"}}},
		{"requests omitted", "spec.resources.requests: Required value",
			map[string]any{"namespaces": []any{"tenant-a"}, "resources": map[string]any{}}},
		{"requests naming only cpu", "spec.resources.requests.memory: Required value",
			map[string]any{"namespaces": []any{"tenant-a"}, "resources": map[string]any{"requests": map[string]any{"cpu": "10m"}}}},
		{"a resources.limits key", "limits: Too many",
			map[string]any{"namespaces": []any{"tenant-a"}, "resources": map[string]any{
				"requests": map[string]any{"cpu": "10m", "memory": "32Mi"},
				"limits":   map[string]any{"cpu": "500m", "memory": "1Gi"},
			}}},
		// maxProperties: 0 rejects any key inside limits but admits an empty
		// object, so the CEL rule is what closes it. Without both, "limits are
		// never written" is a claim the schema only half enforces.
		{"an empty resources.limits", "limits is not supported",
			map[string]any{"namespaces": []any{"tenant-a"}, "resources": map[string]any{
				"requests": map[string]any{"cpu": "10m", "memory": "32Mi"},
				"limits":   map[string]any{},
			}}},
		// The loose regular expression ParseQuantity quotes in its error message
		// accepts "10mm", and the Go conversion then cannot read it. The tight
		// grammar in the schema must refuse it before it is ever stored.
		{"a quantity with a two-character suffix", "spec.resources.requests.cpu in body should match",
			map[string]any{"namespaces": []any{"tenant-a"}, "resources": requests("10mm", "32Mi")}},
		{"a thirteen-character hash", "approvedPlan[0] in body should match",
			map[string]any{"namespaces": []any{"tenant-a"}, "resources": requests("10m", "32Mi"),
				"approvedPlan": []any{"0123456789abc"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejects(t, hardeningGVR, tc.want, object("WorkloadHardening", fmt.Sprintf("hard-%02d", i), tc.spec))
		})
	}
}

// TestHardeningTransitionRule ports the script's five sequenced cases.
// approvedPlan is the only mutable field: arming an existing object is the
// entire workflow and must be accepted (BR-07, FR-01).
func TestHardeningTransitionRule(t *testing.T) {
	const immutable = "only spec.approvedPlan may be changed"
	armed := []any{"0123456789ab", "cafebabe1234"}

	spec := func(extra map[string]any) map[string]any {
		s := map[string]any{"namespaces": []any{"tenant-a", "tenant-b"}, "resources": requests("10m", "32Mi")}
		for k, v := range extra {
			s[k] = v
		}
		return object("WorkloadHardening", "hard-valid", s)
	}

	accepts(t, hardeningGVR, spec(nil))
	accepts(t, hardeningGVR, spec(map[string]any{"approvedPlan": armed}))

	rejects(t, hardeningGVR, immutable, object("WorkloadHardening", "hard-valid", map[string]any{
		"namespaces": []any{"tenant-a", "tenant-c"}, "resources": requests("10m", "32Mi"), "approvedPlan": armed}))
	rejects(t, hardeningGVR, immutable, object("WorkloadHardening", "hard-valid", map[string]any{
		"namespaces": []any{"tenant-a", "tenant-b"}, "resources": requests("500m", "32Mi"), "approvedPlan": armed}))
	rejects(t, hardeningGVR, immutable, spec(map[string]any{
		"securityContext": map[string]any{"readOnlyRootFilesystem": true}, "approvedPlan": armed}))
}
