package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

const uid = "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8"

func ns(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"kubernetes.io/metadata.name": name},
	}}
}

func pod(namespace, name string, labels map[string]string, hostNetwork bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       corev1.PodSpec{HostNetwork: hostNetwork},
	}
}

func netpol(namespace, name string, policyTypes []networkingv1.PolicyType, labels map[string]string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: policyTypes},
	}
}

func iso(aNS string, aLabels map[string]string, bNS string, bLabels map[string]string) *v1alpha1.NetworkIsolation {
	return &v1alpha1.NetworkIsolation{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-dash", Namespace: "isolation-system", UID: uid},
		Spec: v1alpha1.IsolationSpec{Peers: []v1alpha1.Group{
			{Namespace: aNS, PodSelector: metav1.LabelSelector{MatchLabels: aLabels}},
			{Namespace: bNS, PodSelector: metav1.LabelSelector{MatchLabels: bLabels}},
		}},
	}
}

// newReconciler builds a IsolationReconciler over a fake typed clientset seeded with
// objects. The dynamic client is filled in by the reconcile tests; validation
// never touches it.
func newReconciler(objects ...runtime.Object) *IsolationReconciler {
	return &IsolationReconciler{
		Kube: fake.NewSimpleClientset(objects...),
		Protected: map[string]bool{
			"kube-system": true, "kube-public": true, "kube-node-lease": true,
			"isolation-system": true,
		},
		Timeout: 5 * time.Second,
		Now:     func() time.Time { return time.Unix(1700000000, 0) },
	}
}

// AC-05: each precondition failure is a distinct, named reason, and the fake
// records no writes of any kind.
func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name    string
		objects []runtime.Object
		iso     *v1alpha1.NetworkIsolation
		wants   string
	}{
		{
			name:    "protected namespace",
			objects: []runtime.Object{ns("kube-system"), ns("tenant-b")},
			iso:     iso("kube-system", map[string]string{"k8s-app": "kube-dns"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants:   "protected",
		},
		{
			name:    "controller's own namespace",
			objects: []runtime.Object{ns("isolation-system"), ns("tenant-b")},
			iso:     iso("isolation-system", map[string]string{"app": "x"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants:   "protected",
		},
		{
			name:    "missing namespace",
			objects: []runtime.Object{ns("tenant-a")},
			iso:     iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants:   "does not exist",
		},
		{
			name:    "overlapping selectors in a shared namespace",
			objects: []runtime.Object{ns("tenant-x")},
			iso:     iso("tenant-x", map[string]string{"app": "gateway"}, "tenant-x", map[string]string{"tier": "web"}),
			wants:   "disjoint",
		},
		{
			name:    "identical selectors in a shared namespace",
			objects: []runtime.Object{ns("tenant-x")},
			iso:     iso("tenant-x", map[string]string{"app": "gateway"}, "tenant-x", map[string]string{"app": "gateway"}),
			wants:   "disjoint",
		},
		{
			name: "matching hostNetwork pod",
			objects: []runtime.Object{
				ns("tenant-a"), ns("tenant-b"),
				pod("tenant-a", "gw-0", map[string]string{"app": "gateway"}, true),
			},
			iso:   iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants: "hostNetwork",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReconciler(c.objects...)
			_, err := r.validate(context.Background(), c.iso)

			var rej *rejection
			if !errors.As(err, &rej) {
				t.Fatalf("err = %v, want a *rejection", err)
			}
			if !strings.Contains(rej.reason, c.wants) {
				t.Errorf("reason = %q, want it to mention %q", rej.reason, c.wants)
			}
			assertNoWrites(t, r)
		})
	}
}

// The CRD pins spec.peers to exactly two entries, but the controller must not
// panic on an object that predates the constraint or arrives through a path
// that bypassed it.
func TestValidateRejectsWrongPeerCount(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		t.Run(strings.Repeat("peer", 1)+string(rune('0'+n)), func(t *testing.T) {
			object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
			peers := make([]v1alpha1.Group, 0, n)
			for i := range n {
				peers = append(peers, v1alpha1.Group{
					Namespace:   "tenant-" + string(rune('a'+i)),
					PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				})
			}
			object.Spec.Peers = peers

			r := newReconciler(ns("tenant-a"), ns("tenant-b"), ns("tenant-c"))
			_, err := r.validate(context.Background(), object)

			var rej *rejection
			if !errors.As(err, &rej) {
				t.Fatalf("err = %v, want a *rejection", err)
			}
			if !strings.Contains(rej.reason, "exactly two") {
				t.Errorf("reason = %q, want it to mention the required peer count", rej.reason)
			}
			assertNoWrites(t, r)
		})
	}
}

// AC-03: an ingress-affecting foreign policy in either namespace refuses the
// operation; an explicitly egress-only one does not.
func TestValidateForeignPolicies(t *testing.T) {
	cases := []struct {
		name     string
		policy   *networkingv1.NetworkPolicy
		rejected bool
	}{
		{"omitted policyTypes defaults to Ingress", netpol("tenant-b", "legacy", nil, nil), true},
		{"empty policyTypes defaults to Ingress", netpol("tenant-b", "legacy", []networkingv1.PolicyType{}, nil), true},
		{"explicit Ingress", netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, nil), true},
		{"Ingress and Egress", netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeEgress, networkingv1.PolicyTypeIngress}, nil), true},
		{"explicitly egress-only", netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil), false},
		{
			"our own policy from a previous pass",
			netpol("tenant-b", "netiso-"+uid+"-1", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				map[string]string{v1alpha1.OperationLabel: uid}),
			false,
		},
		{
			"another operation's policy is foreign",
			netpol("tenant-b", "netiso-other-1", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				map[string]string{v1alpha1.OperationLabel: "some-other-uid"}),
			true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReconciler(ns("tenant-a"), ns("tenant-b"), c.policy)
			v, err := r.validate(context.Background(),
				iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}))
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if v.contaminated() != c.rejected {
				t.Fatalf("contaminated = %v (%q), want %v", v.contaminated(), v.reasons(), c.rejected)
			}
			if c.rejected {
				// Contamination is scoped to the namespace holding the policy.
				if v.blocked[1] == "" {
					t.Error("tenant-b not marked blocked")
				}
				if v.blocked[0] != "" {
					t.Errorf("tenant-a marked blocked by a policy in tenant-b: %q", v.blocked[0])
				}
			}
			assertNoWrites(t, r)

			// The foreign policy is never touched, whatever the verdict.
			got, getErr := r.Kube.NetworkingV1().NetworkPolicies(c.policy.Namespace).Get(context.Background(), c.policy.Name, metav1.GetOptions{})
			if getErr != nil {
				t.Fatalf("foreign policy disappeared: %v", getErr)
			}
			if len(got.Spec.PolicyTypes) != len(c.policy.Spec.PolicyTypes) {
				t.Error("foreign policy was modified")
			}
		})
	}
}

// Zero-match selectors are valid and report zero (BR-04, AC-06).
func TestValidateCounts(t *testing.T) {
	r := newReconciler(
		ns("tenant-a"), ns("tenant-b"),
		pod("tenant-a", "gw-0", map[string]string{"app": "gateway"}, false),
		pod("tenant-a", "gw-1", map[string]string{"app": "gateway"}, false),
		pod("tenant-a", "other", map[string]string{"app": "cache"}, false),
	)
	c, err := r.validate(context.Background(),
		iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(c.matched) != 2 {
		t.Fatalf("got %d counts, want 2", len(c.matched))
	}
	if c.matched[0] != 2 {
		t.Errorf("matched[0] = %d, want 2", c.matched[0])
	}
	if c.matched[1] != 0 {
		t.Errorf("matched[1] = %d, want 0", c.matched[1])
	}
}

func TestDisjoint(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]string
		want bool
	}{
		{"shared key, different values", map[string]string{"role": "gw"}, map[string]string{"role": "dash"}, true},
		{"shared key, same value", map[string]string{"role": "gw"}, map[string]string{"role": "gw"}, false},
		{"no shared key", map[string]string{"app": "gw"}, map[string]string{"tier": "web"}, false},
		{"one is a superset", map[string]string{"app": "gw"}, map[string]string{"app": "gw", "tier": "web"}, false},
		{"shared key differs among several", map[string]string{"app": "x", "role": "gw"}, map[string]string{"app": "x", "role": "dash"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := disjoint(c.a, c.b); got != c.want {
				t.Errorf("disjoint = %v, want %v", got, c.want)
			}
		})
	}
}

// assertNoWrites fails if the fake clientset recorded anything but reads.
// "Nothing was written" is half of AC-03 and AC-05.
func assertNoWrites(t *testing.T, r *IsolationReconciler) {
	t.Helper()
	for _, a := range r.Kube.(*fake.Clientset).Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("unexpected write: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}

// A selector the CRD's shape rules accept but the API server's label parser
// refuses must be a rejection, not an infinite retry. labels.SelectorFromSet
// performs no validation, so an over-long key reached the server as a 400 and
// wedged the object with a blank phase.
func TestValidateRejectsUnparseableSelector(t *testing.T) {
	cases := []struct {
		name  string
		label map[string]string
	}{
		{"key name over 63 characters", map[string]string{strings.Repeat("a", 64): "v"}},
		{"key prefix over 253 characters", map[string]string{strings.Repeat("a", 254) + "/name": "v"}},
		{"value over 63 characters", map[string]string{"app": strings.Repeat("a", 64)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReconciler(ns("tenant-a"), ns("tenant-b"))
			_, err := r.validate(context.Background(), iso("tenant-a", c.label, "tenant-b", map[string]string{"app": "dashboard"}))

			var rej *rejection
			if !errors.As(err, &rej) {
				t.Fatalf("err = %v, want a *rejection so the object reports why", err)
			}
			if !strings.Contains(rej.reason, "selector") {
				t.Errorf("reason = %q, want it to name the selector as the problem", rej.reason)
			}
			assertNoWrites(t, r)
		})
	}
}
