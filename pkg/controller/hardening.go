package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"
)

// maxNamespaces mirrors the CRD. The schema pins it, but an object stored
// before the constraint tightened, or one that reached etcd another way, must
// not reach the discovery loop (BR-04).
const maxNamespaces = 16

// Reconcile drives one WorkloadHardening, named by its "namespace/name" key.
//
// Per pass: read the object; if the phase is Applied do nothing; validate
// namespaces and LimitRange bounds; enumerate targets; build the plan; then
// preview or apply per BR-07. There is no deletion branch — no finalizer means
// no object is ever stuck waiting on this controller, and deleting the request
// leaves the patches and the provenance annotations in place (FR-05).
func (r *HardeningReconciler) Reconcile(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	u, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Gone. With no finalizer there is nothing to undo: the patches and
		// the provenance annotations stay where they are (FR-05, BR-08). The
		// dry-run cache is this controller's only per-object state, and it is
		// dropped here because nothing else would ever look at it again.
		r.discard(namespace, name)
		return nil
	}
	if err != nil {
		return err
	}

	logger := klog.FromContext(ctx).WithValues("request", string(u.GetUID()), "object", key)

	w, err := v1alpha1.HardeningFromUnstructured(u)
	if err != nil {
		// A stored object whose spec will not convert cannot be planned, and
		// retrying will not change that. Reporting it beats requeueing forever
		// behind a blank status, which is indistinguishable from an object the
		// controller has never seen.
		logger.Info("Rejected", "reason", err.Error())
		stub := &v1alpha1.WorkloadHardening{}
		stub.SetName(u.GetName())
		stub.SetNamespace(u.GetNamespace())
		stub.SetUID(u.GetUID())
		stub.SetResourceVersion(u.GetResourceVersion())
		stub.SetGeneration(u.GetGeneration())
		// The status that is already stored, so setHardeningStatus can see
		// that nothing changed and write nothing. An object stuck here is
		// resynced forever, and FR-06 says an unchanged resync issues no
		// writes at all. status is the one part of this object that does
		// convert; if it somehow does not, the zero value simply means the
		// next write goes ahead.
		if stored, found, err := unstructured.NestedMap(u.Object, "status"); err == nil && found {
			_ = runtime.DefaultUnstructuredConverter.FromUnstructured(stored, &stub.Status)
		}
		// Written through the status subresource, which ignores everything
		// outside status — so the stub's empty spec never reaches etcd.
		return r.setHardeningStatus(ctx, stub, v1alpha1.HardeningStatus{
			Phase:   v1alpha1.PhaseRejected,
			Message: "spec cannot be read: " + err.Error(),
		})
	}

	// Applied is the only terminal phase, including for an object that reached
	// it with targets left Unapproved — but it is terminal only until the
	// approval changes. approvedPlan is the only mutable field in spec, so a
	// generation ahead of the one this status describes is exactly an
	// operator extending or correcting an approval: FR-01's next batch, or a
	// hash that matched nothing being fixed. A resync does not move
	// generation, so an untouched Applied object still issues no API calls
	// (AC-14, AC-18).
	//
	// Everything else is re-evaluated on every resync, so an object refused
	// for a missing namespace or out-of-range bounds recovers by itself once
	// the cause clears. A protected namespace is permanent and will be
	// re-evaluated pointlessly forever; that is accepted, because a second
	// terminality rule costs more than the wasted comparison (FR-05, FR-06).
	if w.Status.Phase == v1alpha1.PhaseApplied && w.Status.ObservedGeneration == w.Generation {
		return nil
	}
	return r.evaluate(ctx, logger, w)
}

// evaluate computes the plan and either previews it or applies it.
func (r *HardeningReconciler) evaluate(ctx context.Context, logger klog.Logger, w *v1alpha1.WorkloadHardening) error {
	policies, err := r.validate(ctx, w)
	var rej *rejection
	if errors.As(err, &rej) {
		logger.Info("Rejected", "reason", rej.reason)
		// Rejected is not terminal, and nothing was written, so there is
		// nothing to report per target.
		return r.setHardeningStatus(ctx, w, v1alpha1.HardeningStatus{
			Phase:   v1alpha1.PhaseRejected,
			Message: rej.reason,
		})
	}
	if err != nil {
		return r.reportFailure(ctx, logger, w, err)
	}

	var (
		rows     []v1alpha1.TargetStatus
		findings []v1alpha1.Finding
		// seen is every target this pass planned a change for, and is what the
		// dry-run cache is pruned against below.
		seen []plan.Target
	)

	// Namespaces in sorted order, and targets within a namespace in
	// kind-then-name order, so a retry resumes predictably and the log reads
	// in the same order as the preview (FR-04).
	for _, namespace := range namespaces(w) {
		targets, namespaceFindings, err := r.discover(ctx, namespace)
		if err != nil {
			return r.reportFailure(ctx, logger, w, err)
		}
		findings = append(findings, namespaceFindings...)

		for _, target := range targets {
			p := plan.Build(target.Pod, policies[namespace])
			for _, f := range p.Findings {
				findings = append(findings, v1alpha1.Finding{
					Namespace: target.Ref.Namespace,
					Kind:      target.Ref.Kind,
					Name:      target.Ref.Name,
					Container: f.Container,
					Reason:    f.Reason,
				})
			}

			if len(p.Changes) == 0 {
				// A target with no gaps yields no patch and no API call — not
				// even a dry-run, and no row in the published plan (FR-02).
				// This is also what makes a retry converge: an already-patched
				// target has no gaps left, so it addresses only what failed.
				continue
			}

			seen = append(seen, target.Ref)
			rows = append(rows, r.execute(ctx, logger, w, target, p.Changes, v1alpha1.TargetStatus{
				Namespace: target.Ref.Namespace,
				Kind:      target.Ref.Kind,
				Name:      target.Ref.Name,
				Hash:      plan.Hash(target.Ref, p.Changes),
				Fields:    plan.Lines(p.Changes),
				Pods:      target.Pods,
				Rollout:   target.Rollout,
			}))
		}
	}

	// Every acceptance for a target this pass did not plan is dead weight: the
	// target is gone, excluded, or has no gaps left. Pruned after the loop so
	// a namespace that failed to list — which returns above — never looks like
	// a namespace with no targets.
	r.retain(w, seen)

	rows = carry(w.Status.Plan, rows)
	phase, message := phaseFor(w, rows)
	logger.Info(string(phase), "message", message, "targets", len(rows), "findings", len(findings))

	if err := r.setHardeningStatus(ctx, w, v1alpha1.HardeningStatus{
		Phase:    phase,
		Message:  message,
		Plan:     rows,
		Findings: findings,
	}); err != nil {
		return err
	}
	// Only an apply requeues. FR-03: a refusal during a preview leaves the
	// object Previewed with the refusal on its row, and the periodic resync
	// retries it — returning an error here would spin the queue's backoff
	// against a webhook that may refuse permanently, on behalf of an object
	// nobody armed. During an apply the same refusal is a Failed target and
	// does return an error, per FR-04.
	if !w.Armed() {
		return nil
	}
	return retryable(rows)
}

// reportFailure records a transient API failure and returns it so the queue
// retries. An object with a blank phase is indistinguishable from one the
// controller has never seen, so an operator cannot tell a wedged reconcile
// from a controller that is not running (NFR-04).
func (r *HardeningReconciler) reportFailure(ctx context.Context, logger klog.Logger, w *v1alpha1.WorkloadHardening, cause error) error {
	if statusErr := r.setHardeningStatus(ctx, w, v1alpha1.HardeningStatus{
		Phase:    v1alpha1.PhasePending,
		Message:  cause.Error(),
		Plan:     w.Status.Plan,
		Findings: w.Status.Findings,
	}); statusErr != nil {
		logger.Error(statusErr, "Could not report the failure")
	}
	return cause
}

// namespaces returns the named namespaces, sorted and deduplicated. Sorted so
// a retry resumes predictably and the log reads in the same order as the
// preview (FR-04); deduplicated because the CRD's list-type: set is the only
// other guard, and an object stored before that constraint would otherwise
// contribute every row and every finding of the repeated namespace twice.
func namespaces(w *v1alpha1.WorkloadHardening) []string {
	return slices.Compact(slices.Sorted(slices.Values(w.Spec.Namespaces)))
}

// validate checks every precondition that can refuse the whole object before a
// single target is read.
//
// It is all or nothing across namespaces: if one named namespace is missing or
// refused, the object is rejected and no namespace is patched. The operator
// named two namespaces and should get both or neither. Execution, in contrast,
// is per target (FR-05, BR-07).
func (r *HardeningReconciler) validate(ctx context.Context, w *v1alpha1.WorkloadHardening) (map[string]plan.Policy, error) {
	if n := len(w.Spec.Namespaces); n == 0 || n > maxNamespaces {
		return nil, reject("spec.namespaces holds %d entries; between 1 and %d are required", n, maxNamespaces)
	}

	// Both are required by the schema, and both are re-read here because a
	// missing one would otherwise become a silent zero quantity written into
	// every container.
	requests := corev1.ResourceList{}
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		quantity, ok := w.Spec.Resources.Requests[name]
		if !ok {
			return nil, reject("spec.resources.requests.%s is required", name)
		}
		requests[name] = quantity
	}

	// BR-05, before any API call: naming a protected namespace rejects the
	// whole object, so nothing is previewed and nothing is written.
	for _, namespace := range w.Spec.Namespaces {
		if r.Protected[namespace] {
			return nil, reject("namespace %q is protected and may not be targeted", namespace)
		}
	}

	policies := map[string]plan.Policy{}
	for _, namespace := range namespaces(w) {
		if _, err := r.Kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, reject("namespace %q does not exist", namespace)
			}
			return nil, err
		}

		limitRanges, err := r.Kube.CoreV1().LimitRanges(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		coverage, why := plan.Cover(limitRanges.Items, requests)
		if why != "" {
			return nil, reject("namespace %q: %s", namespace, why)
		}

		policies[namespace] = plan.Policy{
			Requests:               requests,
			ReadOnlyRootFilesystem: w.Spec.SecurityContext.ReadOnlyRootFilesystem,
			Coverage:               coverage,
		}
	}
	return policies, nil
}

// setHardeningStatus writes status only when something other than the
// timestamp changed, so a resync of an unchanged object issues no writes at
// all (FR-06), exactly as in 001. lastReconcileTime therefore records when the
// observation last changed, not when the last pass ran.
func (r *HardeningReconciler) setHardeningStatus(ctx context.Context, w *v1alpha1.WorkloadHardening, want v1alpha1.HardeningStatus) error {
	// Stamped on every write: the status describes the spec that produced it,
	// and FR-05's terminality gate compares the two. Set before the equality
	// check so a pass that changes nothing but the generation still persists
	// it — otherwise an approval edit that turns out to be a no-op would leave
	// the object re-evaluating itself forever.
	want.ObservedGeneration = w.Generation
	// Derived rather than passed in, so no caller can publish a count that
	// disagrees with the spec it describes.
	want.ApprovedCount = len(w.Spec.ApprovedPlan)
	want.NamespaceCount = len(namespaces(w))
	want.LastReconcileTime = w.Status.LastReconcileTime
	if equality.Semantic.DeepEqual(w.Status, want) {
		return nil
	}
	want.LastReconcileTime = r.Now().UTC().Format(time.RFC3339)
	w.Status = want

	u, err := v1alpha1.HardeningToUnstructured(w)
	if err != nil {
		return err
	}
	out, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(w.Namespace).
		UpdateStatus(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	fresh, err := v1alpha1.HardeningFromUnstructured(out)
	if err != nil {
		return err
	}
	*w = *fresh
	return nil
}

// retryable returns an error when a pass left work that a retry could finish.
//
// Only a failure does. Stale is not retryable — recomputing produces the same
// hash and the same refusal until the operator updates the approval — and
// Unapproved is not a failure at all: approving a subset is the expected use
// of a per-target gate (FR-04, FR-06). Returning an error for either would
// spin the queue's backoff forever over a state only a human can clear.
// It is reached only on an armed pass; see the caller.
func retryable(rows []v1alpha1.TargetStatus) error {
	var failed int
	for _, row := range rows {
		if row.Outcome == v1alpha1.OutcomeFailed {
			failed++
		}
	}
	if failed == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d targets failed", failed, len(rows))
}
