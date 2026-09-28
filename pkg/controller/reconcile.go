package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/policy"
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

// Reconcile drives one NetworkIsolation, named by its "namespace/name" key,
// towards what its spec asks for. Desired state is re-derived from the spec on
// every pass, so a crash between writes is repaired by the next one (FR-04).
func (r *Reconciler) Reconcile(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	u, err := r.Dyn.Resource(v1alpha1.Resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Gone, and the finalizer guaranteed cleanup ran before it went.
		return nil
	}
	if err != nil {
		return err
	}
	iso, err := v1alpha1.FromUnstructured(u)
	if err != nil {
		return err
	}

	logger := klog.FromContext(ctx).WithValues("operation", string(iso.UID), "object", key)

	// Deletion always takes precedence over activation, so a restart
	// mid-deletion resumes rather than recreating policies (FR-04).
	if iso.DeletionTimestamp != nil {
		return r.cleanup(ctx, logger, iso)
	}
	return r.activate(ctx, logger, iso)
}

// activate validates, persists the finalizer, writes both policies and reports.
func (r *Reconciler) activate(ctx context.Context, logger klog.Logger, iso *v1alpha1.NetworkIsolation) error {
	c, err := r.validate(ctx, iso)
	var rej *rejection
	if errors.As(err, &rej) {
		// Before activation, a failed precondition means nothing was written:
		// Rejected. After it — the finalizer is the marker — the policies are
		// already in place, they stay there, and it is the assumption behind
		// them that no longer holds: Degraded, retried until it holds again.
		phase := v1alpha1.PhaseRejected
		if slices.Contains(iso.Finalizers, v1alpha1.Finalizer) {
			phase = v1alpha1.PhaseDegraded
		}
		logger.Info(string(phase), "reason", rej.reason)
		if err := r.setStatus(ctx, iso, v1alpha1.Status{
			Phase:   phase,
			Message: rej.reason,
			Peers:   iso.Status.Peers,
		}); err != nil {
			return err
		}
		if phase == v1alpha1.PhaseDegraded {
			return fmt.Errorf("precondition no longer holds: %s", rej.reason)
		}
		return nil
	}
	if err != nil {
		return err
	}

	// Nothing is written until cleanup is guaranteed a chance to run (FR-03).
	if !slices.Contains(iso.Finalizers, v1alpha1.Finalizer) {
		iso.Finalizers = append(iso.Finalizers, v1alpha1.Finalizer)
		if err := r.update(ctx, iso); err != nil {
			return err
		}
	}

	desired := policy.Build(iso)
	peers := make([]v1alpha1.PeerStatus, len(desired))
	var failures []string
	for i, want := range desired {
		peers[i] = v1alpha1.PeerStatus{Policy: want.Name, Matched: c[i]}
		if err := r.applyPolicy(ctx, want, string(iso.UID)); err != nil {
			// Whatever succeeded stays: rolling back would reopen traffic
			// this operation was asked to block (FR-04).
			logger.Error(err, "Policy write failed", "policy", want.Namespace+"/"+want.Name)
			failures = append(failures, fmt.Sprintf("%s/%s: %v", want.Namespace, want.Name, err))
		}
	}

	status := v1alpha1.Status{Phase: v1alpha1.PhaseActive, Peers: peers}
	if len(failures) > 0 {
		status.Phase = v1alpha1.PhaseDegraded
		status.Message = strings.Join(failures, "; ")
	}
	if err := r.setStatus(ctx, iso, status); err != nil {
		return err
	}
	if len(failures) > 0 {
		// Returned so the queue retries; the status already says why.
		return fmt.Errorf("%d of %d policies not written", len(failures), len(desired))
	}
	logger.Info("Active", "peers", peers)
	return nil
}

// applyPolicy creates or updates one owned policy. A policy carrying someone
// else's operation label under the same name is reported, never overwritten
// (FR-03). When the stored spec already matches, no write is issued at all, so
// a steady state costs nothing (AC-06).
func (r *Reconciler) applyPolicy(ctx context.Context, want *networkingv1.NetworkPolicy, uid string) error {
	api := r.Kube.NetworkingV1().NetworkPolicies(want.Namespace)
	got, err := api.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, want, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if got.Labels[v1alpha1.OperationLabel] != uid {
		return fmt.Errorf("name is occupied by policy %s/%s, which this operation does not own", want.Namespace, want.Name)
	}
	if equality.Semantic.DeepEqual(got.Spec, want.Spec) {
		return nil
	}
	got.Spec = want.Spec
	got.Labels = want.Labels
	got.Annotations = want.Annotations
	_, err = api.Update(ctx, got, metav1.UpdateOptions{})
	return err
}

// setStatus writes status only when something other than the timestamp changed,
// so reconciling an unchanged object produces no writes at all (AC-06).
// lastReconcileTime therefore records when the observation last changed, not
// when the last pass ran.
func (r *Reconciler) setStatus(ctx context.Context, iso *v1alpha1.NetworkIsolation, want v1alpha1.Status) error {
	want.LastReconcileTime = iso.Status.LastReconcileTime
	if equality.Semantic.DeepEqual(iso.Status, want) {
		return nil
	}
	want.LastReconcileTime = r.Now().UTC().Format(time.RFC3339)
	iso.Status = want

	u, err := v1alpha1.ToUnstructured(iso)
	if err != nil {
		return err
	}
	out, err := r.Dyn.Resource(v1alpha1.Resource).Namespace(iso.Namespace).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return refresh(iso, out)
}

// update persists a metadata change — only ever the finalizer list — and
// refreshes iso with the stored version, so the status write that follows in
// the same pass is not a conflict.
func (r *Reconciler) update(ctx context.Context, iso *v1alpha1.NetworkIsolation) error {
	u, err := v1alpha1.ToUnstructured(iso)
	if err != nil {
		return err
	}
	out, err := r.Dyn.Resource(v1alpha1.Resource).Namespace(iso.Namespace).Update(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return refresh(iso, out)
}

func refresh(iso *v1alpha1.NetworkIsolation, from *unstructured.Unstructured) error {
	fresh, err := v1alpha1.FromUnstructured(from)
	if err != nil {
		return err
	}
	*iso = *fresh
	return nil
}

// cleanup removes the policies this operation owns, then the finalizer. It must
// not depend on matching pods, namespaces or preconditions still being valid
// (FR-03). Cross-namespace owner references do not work, so this is explicit.
func (r *Reconciler) cleanup(ctx context.Context, logger klog.Logger, iso *v1alpha1.NetworkIsolation) error {
	if !slices.Contains(iso.Finalizers, v1alpha1.Finalizer) {
		// Nothing was ever written under this operation, so there is nothing
		// to undo and nothing holding the object back.
		return nil
	}

	if err := r.setStatus(ctx, iso, v1alpha1.Status{
		Phase: v1alpha1.PhaseDeleting,
		Peers: iso.Status.Peers,
	}); err != nil {
		return err
	}

	names := policy.Names(string(iso.UID))
	for i, g := range iso.Spec.Peers {
		if i >= len(names) {
			break
		}
		if err := r.deletePolicy(ctx, g.Namespace, names[i], string(iso.UID)); err != nil {
			// An API error is not evidence the policy is gone. Keep the
			// finalizer and retry (FR-03).
			logger.Error(err, "Cleanup incomplete, finalizer retained", "policy", g.Namespace+"/"+names[i])
			return err
		}
	}

	iso.Finalizers = slices.DeleteFunc(iso.Finalizers, func(f string) bool { return f == v1alpha1.Finalizer })
	logger.Info("Cleaned up", "policies", names)
	return r.update(ctx, iso)
}

// deletePolicy removes one policy if this operation owns it. An already absent
// policy is success; a policy under the same name owned by anyone else is left
// untouched.
func (r *Reconciler) deletePolicy(ctx context.Context, namespace, name, uid string) error {
	api := r.Kube.NetworkingV1().NetworkPolicies(namespace)
	got, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if got.Labels[v1alpha1.OperationLabel] != uid {
		return nil
	}
	if err := api.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
