// Package policy turns a NetworkIsolation spec into the two NetworkPolicies
// that implement it. It is pure — no clients, no context, no clock — so the
// hard part of this feature is testable on its own (NFR-01).
package policy

import (
	"maps"
	"slices"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// namespaceNameLabel is applied to every namespace by the API server since
// v1.21, which is what lets a peer name a namespace without the operator
// having to label it.
const namespaceNameLabel = "kubernetes.io/metadata.name"

// Names returns the deterministic policy names for an operation: the policy
// protecting group A, then the one protecting group B (FR-03). A UID is 36
// characters of lowercase hex and hyphens, so the result is always a valid
// DNS-1123 subdomain and needs no hashing or truncation.
func Names(uid string) (string, string) {
	return "netiso-" + uid + "-a", "netiso-" + uid + "-b"
}

// Build returns the two policies for iso: element 0 protects group A and lives
// in A's namespace, element 1 protects group B and lives in B's.
func Build(iso *v1alpha1.NetworkIsolation) []*networkingv1.NetworkPolicy {
	uid := string(iso.UID)
	owner := iso.Namespace + "/" + iso.Name
	nameA, nameB := Names(uid)
	return []*networkingv1.NetworkPolicy{
		build(nameA, uid, owner, iso.Spec.A, iso.Spec.B),
		build(nameB, uid, owner, iso.Spec.B, iso.Spec.A),
	}
}

// build produces the ingress-only policy that protects self by allowing every
// pod source except other. NetworkPolicy has no deny rule, so a prohibition is
// written as an allowance of the complement (FR-02).
func build(name, uid, owner string, self, other v1alpha1.Group) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   self.Namespace,
			Labels:      map[string]string{v1alpha1.OperationLabel: uid},
			Annotations: map[string]string{v1alpha1.OwnerAnnotation: owner},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: self.PodSelector,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: complement(other)}},
		},
	}
}

// complement lists the peers whose union is exactly "every pod that is not g".
func complement(g v1alpha1.Group) []networkingv1.NetworkPolicyPeer {
	peers := []networkingv1.NetworkPolicyPeer{{
		// Branch 1: every pod in every namespace other than g's. A peer with
		// only a namespaceSelector means all pods in the matching namespaces.
		NamespaceSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      namespaceNameLabel,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{g.Namespace},
			}},
		},
	}}

	// Branch 2, one peer per requirement: inside g's namespace, the pods that
	// fail that requirement. NotIn also matches a pod that lacks the key
	// entirely, which is what makes the union complete. The namespaceSelector
	// is not optional here: the policy lives in the *other* group's namespace,
	// so a bare podSelector would select the wrong namespace.
	//
	// Keys are sorted so repeated reconciles of an unchanged object produce a
	// byte-identical spec and therefore no write (AC-06).
	for _, k := range slices.Sorted(maps.Keys(g.PodSelector.MatchLabels)) {
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{namespaceNameLabel: g.Namespace},
			},
			PodSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      k,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{g.PodSelector.MatchLabels[k]},
				}},
			},
		})
	}
	return peers
}
