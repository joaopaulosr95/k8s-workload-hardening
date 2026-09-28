package policy

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

const testUID = "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8"

func isolation(aNS string, aLabels map[string]string, bNS string, bLabels map[string]string) *v1alpha1.NetworkIsolation {
	return &v1alpha1.NetworkIsolation{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-dash", Namespace: "isolation-system", UID: testUID},
		Spec: v1alpha1.Spec{
			A: v1alpha1.Group{Namespace: aNS, PodSelector: metav1.LabelSelector{MatchLabels: aLabels}},
			B: v1alpha1.Group{Namespace: bNS, PodSelector: metav1.LabelSelector{MatchLabels: bLabels}},
		},
	}
}

// AC-01: exactly two ingress-only policies, each in its own group's namespace,
// each selecting its own group, with no egress and no ipBlock anywhere.
func TestBuildShape(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"app": "dashboard", "tier": "web"})

	got := Build(iso)
	if len(got) != 2 {
		t.Fatalf("got %d policies, want 2", len(got))
	}

	nameA, nameB := Names(testUID)
	cases := []struct {
		p        *networkingv1.NetworkPolicy
		name, ns string
		selector map[string]string
		peers    int
	}{
		{got[0], nameA, "tenant-a", map[string]string{"app": "gateway"}, 3},                  // 1 + len(B labels)
		{got[1], nameB, "tenant-b", map[string]string{"app": "dashboard", "tier": "web"}, 2}, // 1 + len(A labels)
	}
	for _, c := range cases {
		if c.p.Name != c.name || c.p.Namespace != c.ns {
			t.Errorf("policy = %s/%s, want %s/%s", c.p.Namespace, c.p.Name, c.ns, c.name)
		}
		if c.p.Labels[v1alpha1.OperationLabel] != testUID {
			t.Errorf("%s: operation label = %q", c.p.Name, c.p.Labels[v1alpha1.OperationLabel])
		}
		if c.p.Annotations[v1alpha1.OwnerAnnotation] != "isolation-system/gw-dash" {
			t.Errorf("%s: owner annotation = %q", c.p.Name, c.p.Annotations[v1alpha1.OwnerAnnotation])
		}
		if len(c.p.Spec.PolicyTypes) != 1 || c.p.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
			t.Errorf("%s: policyTypes = %v, want [Ingress]", c.p.Name, c.p.Spec.PolicyTypes)
		}
		if c.p.Spec.Egress != nil {
			t.Errorf("%s: egress rules present", c.p.Name)
		}
		for k, v := range c.selector {
			if c.p.Spec.PodSelector.MatchLabels[k] != v {
				t.Errorf("%s: podSelector = %v, want %v", c.p.Name, c.p.Spec.PodSelector.MatchLabels, c.selector)
			}
		}
		if len(c.p.Spec.Ingress) != 1 {
			t.Fatalf("%s: got %d ingress rules, want 1", c.p.Name, len(c.p.Spec.Ingress))
		}
		if c.p.Spec.Ingress[0].Ports != nil {
			t.Errorf("%s: port restrictions present, want none (BR-01)", c.p.Name)
		}
		peers := c.p.Spec.Ingress[0].From
		if len(peers) != c.peers {
			t.Errorf("%s: got %d peers, want %d", c.p.Name, len(peers), c.peers)
		}
		for _, peer := range peers {
			if peer.IPBlock != nil {
				t.Errorf("%s: ipBlock present (FR-02 forbids it)", c.p.Name)
			}
		}
	}
}

// allowed reports whether a pod in namespace ns carrying ls is matched by any
// of the peers — that is, whether the generated policy would admit it.
func allowed(t *testing.T, peers []networkingv1.NetworkPolicyPeer, ns string, ls map[string]string) bool {
	t.Helper()
	for _, peer := range peers {
		nsSel, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			t.Fatalf("namespaceSelector does not parse: %v", err)
		}
		if !nsSel.Matches(labels.Set{"kubernetes.io/metadata.name": ns}) {
			continue
		}
		if peer.PodSelector == nil {
			return true // whole namespace
		}
		podSel, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
		if err != nil {
			t.Fatalf("podSelector does not parse: %v", err)
		}
		if podSel.Matches(labels.Set(ls)) {
			return true
		}
	}
	return false
}

// AC-02: the complement of B admits everything that is not B, and admits
// nothing that is. The missing-key row is the one that catches a negation
// written as "different value" instead of "fails the requirement".
func TestComplementTruthTable(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"app": "dashboard", "tier": "web"})
	peers := Build(iso)[0].Spec.Ingress[0].From // protects A, excludes B

	cases := []struct {
		name    string
		ns      string
		labels  map[string]string
		allowed bool
	}{
		{"B itself is excluded", "tenant-b", map[string]string{"app": "dashboard", "tier": "web"}, false},
		{"B plus extra labels is still B", "tenant-b", map[string]string{"app": "dashboard", "tier": "web", "x": "y"}, false},
		{"different value for one key", "tenant-b", map[string]string{"app": "worker", "tier": "web"}, true},
		{"missing one key entirely", "tenant-b", map[string]string{"app": "dashboard"}, true},
		{"missing both keys", "tenant-b", nil, true},
		{"unrelated pod in B's namespace", "tenant-b", map[string]string{"app": "cache"}, true},
		{"same labels but another namespace", "tenant-c", map[string]string{"app": "dashboard", "tier": "web"}, true},
		{"a pod in A's own namespace", "tenant-a", map[string]string{"app": "gateway"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := allowed(t, peers, c.ns, c.labels); got != c.allowed {
				t.Errorf("allowed=%v, want %v", got, c.allowed)
			}
		})
	}
}

// AC-02, shared-namespace half: both groups in one namespace must still
// complement each other, and neither policy may exclude the other's neighbours.
func TestComplementSharedNamespace(t *testing.T) {
	iso := isolation("tenant-x", map[string]string{"role": "gateway"},
		"tenant-x", map[string]string{"role": "dashboard"})
	built := Build(iso)
	protectsA, protectsB := built[0].Spec.Ingress[0].From, built[1].Spec.Ingress[0].From

	if allowed(t, protectsA, "tenant-x", map[string]string{"role": "dashboard"}) {
		t.Error("policy protecting A admits B")
	}
	if allowed(t, protectsB, "tenant-x", map[string]string{"role": "gateway"}) {
		t.Error("policy protecting B admits A")
	}
	if !allowed(t, protectsA, "tenant-x", map[string]string{"role": "gateway"}) {
		t.Error("policy protecting A blocks A's own pods")
	}
	if !allowed(t, protectsA, "tenant-x", map[string]string{"role": "cache"}) {
		t.Error("policy protecting A blocks an unrelated pod in the shared namespace")
	}
	if !allowed(t, protectsA, "tenant-y", map[string]string{"role": "dashboard"}) {
		t.Error("policy protecting A blocks a same-labelled pod in another namespace")
	}
}

// Label keys may carry a DNS-subdomain prefix. The generated requirement must
// use the key verbatim rather than mangling the slash.
func TestPrefixedLabelKey(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"example.com/tier": "gold"})
	peers := Build(iso)[0].Spec.Ingress[0].From

	if allowed(t, peers, "tenant-b", map[string]string{"example.com/tier": "gold"}) {
		t.Error("prefixed-key group not excluded")
	}
	if !allowed(t, peers, "tenant-b", map[string]string{"example.com/tier": "silver"}) {
		t.Error("different value for a prefixed key should be allowed")
	}
	if !allowed(t, peers, "tenant-b", map[string]string{"tier": "gold"}) {
		t.Error("unprefixed key is a different key and should be allowed")
	}
}

// An empty string is a legal label value. The NotIn requirement built from it
// must parse and must exclude only pods carrying that exact empty value.
func TestEmptyLabelValue(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"app": ""})
	peers := Build(iso)[0].Spec.Ingress[0].From

	if allowed(t, peers, "tenant-b", map[string]string{"app": ""}) {
		t.Error("pod carrying the empty value was not excluded")
	}
	if !allowed(t, peers, "tenant-b", map[string]string{"app": "dashboard"}) {
		t.Error("pod with a non-empty value should be allowed")
	}
	if !allowed(t, peers, "tenant-b", nil) {
		t.Error("pod lacking the key should be allowed")
	}
}

// Peers are emitted in sorted key order. Go randomises map iteration, so
// without the sort two reconciles of an unchanged object generate different
// specs, the reconciler sees a difference that isn't one, and it rewrites the
// policy on every pass. AC-06's idempotency test uses single-key selectors and
// cannot catch this; it has to be pinned where the ordering is decided.
func TestComplementPeerOrderIsDeterministic(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"tier": "web", "app": "dashboard", "zone": "eu", "role": "ui"})

	wantKeys := []string{"app", "role", "tier", "zone"} // sorted
	for attempt := range 20 {
		peers := Build(iso)[0].Spec.Ingress[0].From
		if len(peers) != 1+len(wantKeys) {
			t.Fatalf("attempt %d: got %d peers, want %d", attempt, len(peers), 1+len(wantKeys))
		}
		for i, want := range wantKeys {
			got := peers[i+1].PodSelector.MatchExpressions[0].Key // peer 0 is the other-namespaces branch
			if got != want {
				t.Fatalf("attempt %d: peer %d key = %q, want %q (peers must be in sorted key order)", attempt, i+1, got, want)
			}
		}
	}
}
