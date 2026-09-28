package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// Reconciler carries everything one reconcile pass needs. It holds client
// interfaces rather than concrete clients, so both fakes drive it directly.
type Reconciler struct {
	Kube      kubernetes.Interface
	Dyn       dynamic.Interface
	Protected map[string]bool
	Timeout   time.Duration
	Now       func() time.Time
}

// rejection is a precondition failure. Retrying will not help until the cluster
// or the spec changes, so it becomes a Rejected status rather than a requeue.
// Any other error is an API failure and is retried.
type rejection struct{ reason string }

func (e *rejection) Error() string { return e.reason }

func reject(format string, args ...any) error {
	return &rejection{reason: fmt.Sprintf(format, args...)}
}

// counts is how many pods each peer currently matches, index-aligned with
// spec.peers.
type counts []int

// validate checks every precondition in BR-03, BR-04 and BR-05 before anything
// is written, and returns the matched pod count per peer.
func (r *Reconciler) validate(ctx context.Context, iso *v1alpha1.NetworkIsolation) (counts, error) {
	peers := iso.Spec.Peers

	// The CRD pins this, but an object stored before the constraint tightened,
	// or one that reached etcd another way, must not reach the policy builder.
	if len(peers) != 2 {
		return nil, reject("spec.peers holds %d entries; exactly two are required", len(peers))
	}

	// BR-05, checked before any API call: never target a protected namespace.
	for _, g := range peers {
		if r.Protected[g.Namespace] {
			return nil, reject("namespace %q is protected and may not be targeted", g.Namespace)
		}
	}

	// BR-04: two peers in one namespace must be provably disjoint, or one
	// policy's complement would block the other peer's own pods.
	if peers[0].Namespace == peers[1].Namespace &&
		!disjoint(peers[0].PodSelector.MatchLabels, peers[1].PodSelector.MatchLabels) {
		return nil, reject(
			"both peers target namespace %q but their selectors are not provably disjoint: no label key is held by both with different values",
			peers[0].Namespace)
	}

	out := make(counts, len(peers))
	checked := map[string]bool{}
	for i, g := range peers {
		if _, err := r.Kube.CoreV1().Namespaces().Get(ctx, g.Namespace, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, reject("namespace %q does not exist", g.Namespace)
			}
			return nil, err
		}

		n, err := r.countPods(ctx, g)
		if err != nil {
			return nil, err
		}
		out[i] = n

		// One namespace, one foreign-policy scan, even when both peers share it.
		if checked[g.Namespace] {
			continue
		}
		checked[g.Namespace] = true
		if err := r.checkForeignPolicies(ctx, g.Namespace, string(iso.UID)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// disjoint reports whether two equality selectors can never match the same pod:
// they share a key and disagree on its value (BR-04). A superset relationship
// is not disjoint — a pod can satisfy both.
func disjoint(a, b map[string]string) bool {
	for k, va := range a {
		if vb, ok := b[k]; ok && vb != va {
			return true
		}
	}
	return false
}

// countPods returns how many pods g currently matches, rejecting if any of them
// runs on the host network, where NetworkPolicy behaviour is undefined (BR-04).
func (r *Reconciler) countPods(ctx context.Context, g v1alpha1.Group) (int, error) {
	list, err := r.Kube.CoreV1().Pods(g.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(g.PodSelector.MatchLabels).String(),
	})
	if err != nil {
		return 0, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.HostNetwork {
			return 0, reject("pod %s/%s matches the selector and uses hostNetwork, for which NetworkPolicy behaviour is undefined upstream",
				g.Namespace, list.Items[i].Name)
		}
	}
	return len(list.Items), nil
}

// checkForeignPolicies refuses to operate in a namespace holding any
// ingress-affecting policy this operation does not own. NetworkPolicy is
// additive: an allow rule placed beside an existing ingress policy widens
// access instead of narrowing it (BR-03).
func (r *Reconciler) checkForeignPolicies(ctx context.Context, namespace, uid string) error {
	list, err := r.Kube.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range list.Items {
		p := &list.Items[i]
		if p.Labels[v1alpha1.OperationLabel] == uid {
			continue
		}
		if affectsIngress(p) {
			return reject("namespace %q already contains ingress policy %q, which this operation does not own", namespace, p.Name)
		}
	}
	return nil
}

// affectsIngress applies the policyTypes defaulting rule: an omitted or empty
// policyTypes always implies Ingress, so only an explicit egress-only list is
// safe to ignore.
func affectsIngress(p *networkingv1.NetworkPolicy) bool {
	if len(p.Spec.PolicyTypes) == 0 {
		return true
	}
	return slices.Contains(p.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
}
