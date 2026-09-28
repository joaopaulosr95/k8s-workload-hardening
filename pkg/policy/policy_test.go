package policy

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
