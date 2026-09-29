package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// rejection is a precondition failure. Retrying will not help until the cluster
// or the spec changes, so it becomes a Rejected status rather than a requeue.
// Any other error is an API failure and is retried.
type rejection struct{ reason string }

func (e *rejection) Error() string { return e.reason }

func reject(format string, args ...any) error {
	return &rejection{reason: fmt.Sprintf(format, args...)}
}

// validation is what one pass observed about the two peers, index-aligned with
// spec.peers.
type validation struct {
	// matched is how many pods each peer currently matches.
	matched []int
	// blocked[i] is non-empty when peer i's namespace holds an ingress policy
	// this operation does not own. Contamination is per-namespace: it is a
	// reason not to write there, not a reason to stop maintaining the other
	// peer, whose policy is half the block.
	blocked []string
}

// contaminated reports whether any peer's namespace holds a foreign ingress
// policy.
func (v validation) contaminated() bool {
	return slices.ContainsFunc(v.blocked, func(s string) bool { return s != "" })
}

// reasons joins the per-peer contamination messages.
func (v validation) reasons() string {
	var out []string
	for _, b := range v.blocked {
		if b != "" {
			out = append(out, b)
		}
	}
	return strings.Join(out, "; ")
}

// validate checks every precondition in BR-03, BR-04 and BR-05 before anything
// is written, and returns the matched pod count per peer.
func (r *IsolationReconciler) validate(ctx context.Context, iso *v1alpha1.NetworkIsolation) (validation, error) {
	peers := iso.Spec.Peers

	// The CRD pins this, but an object stored before the constraint tightened,
	// or one that reached etcd another way, must not reach the policy builder.
	if len(peers) != 2 {
		return validation{}, reject("spec.peers holds %d entries; exactly two are required", len(peers))
	}

	// BR-05, checked before any API call: never target a protected namespace.
	for _, g := range peers {
		if r.Protected[g.Namespace] {
			return validation{}, reject("namespace %q is protected and may not be targeted", g.Namespace)
		}
	}

	// BR-04: two peers in one namespace must be provably disjoint, or one
	// policy's complement would block the other peer's own pods.
	if peers[0].Namespace == peers[1].Namespace &&
		!disjoint(peers[0].PodSelector.MatchLabels, peers[1].PodSelector.MatchLabels) {
		return validation{}, reject(
			"both peers target namespace %q but their selectors are not provably disjoint: no label key is held by both with different values",
			peers[0].Namespace)
	}

	out := validation{matched: make([]int, len(peers)), blocked: make([]string, len(peers))}
	scanned := map[string]string{}
	for i, g := range peers {
		if _, err := r.Kube.CoreV1().Namespaces().Get(ctx, g.Namespace, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return validation{}, reject("namespace %q does not exist", g.Namespace)
			}
			return validation{}, err
		}

		n, err := r.countPods(ctx, g)
		if err != nil {
			return validation{}, err
		}
		out.matched[i] = n

		// One namespace, one foreign-policy scan, even when both peers share it.
		reason, seen := scanned[g.Namespace]
		if !seen {
			if reason, err = r.foreignIngressPolicy(ctx, g.Namespace, string(iso.UID)); err != nil {
				return validation{}, err
			}
			scanned[g.Namespace] = reason
		}
		out.blocked[i] = reason
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
func (r *IsolationReconciler) countPods(ctx context.Context, g v1alpha1.Group) (int, error) {
	// The CRD validates the shape of a label key but not its length, and
	// labels.SelectorFromSet performs no validation at all — an over-long key
	// would reach the API server as a 400, which is not a precondition failure
	// and would retry forever with nothing in status to explain it.
	selector, err := metav1.LabelSelectorAsSelector(&g.PodSelector)
	if err != nil {
		return 0, reject("the pod selector for namespace %q is not a valid label selector: %v", g.Namespace, err)
	}
	list, err := r.Kube.CoreV1().Pods(g.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
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

// foreignIngressPolicy returns a reason string naming the first
// ingress-affecting policy in the namespace that this operation does not own,
// or "" if there is none. NetworkPolicy is additive: an allow rule placed
// beside an existing ingress policy widens access instead of narrowing it
// (BR-03), so this operation will not write into such a namespace.
func (r *IsolationReconciler) foreignIngressPolicy(ctx context.Context, namespace, uid string) (string, error) {
	list, err := r.Kube.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	for i := range list.Items {
		p := &list.Items[i]
		if p.Labels[v1alpha1.OperationLabel] == uid {
			continue
		}
		if affectsIngress(p) {
			return fmt.Sprintf("namespace %q already contains ingress policy %q, which this operation does not own", namespace, p.Name), nil
		}
	}
	return "", nil
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
