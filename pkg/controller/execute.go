package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"
)

// dryRun is the option set that makes the API server complete every processing
// stage and then discard the result, so the preview reflects what the API
// server accepts rather than what the tool believes it would accept (FR-03).
//
// It does not cover the failures BR-02 is about. The API server accepts
// runAsNonRoot: true alongside runAsUser: 0 without complaint, and accepts a
// privileged root container under a pod-level runAsNonRoot: true; the pod then
// fails with CreateContainerConfigError. Every root-related failure is
// enforced by the kubelet or the kernel, not by API validation.
func dryRun() metav1.PatchOptions {
	return metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}}
}

// execute decides one target's fate and carries it out. Nothing outside this
// function writes to a workload.
func (r *HardeningReconciler) execute(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	changes []plan.Change,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	body, err := plan.Patch(changes)
	if err != nil {
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = err.Error()
		return row
	}

	if !w.Armed() {
		return r.preview(ctx, logger, w, t, body, row)
	}
	return r.apply(ctx, logger, w, t, body, row)
}

// preview dry-runs a changed target and publishes its hash. Nothing is written.
//
// While the object is unarmed, a target whose change hash is unchanged since
// the last pass is not re-sent: otherwise a Previewed object re-runs the full
// admission chain, every mutating and validating webhook included, for every
// target in up to sixteen namespaces on every resync, forever, on behalf of an
// object nobody armed (FR-03).
func (r *HardeningReconciler) preview(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	body []byte,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	row.Outcome = v1alpha1.OutcomePlanned
	if r.cached(w, t.Ref, row.Hash) {
		return row
	}
	if err := t.patch(ctx, body, dryRun()); err != nil {
		// The API server refuses a dry-run that would reach a webhook
		// declaring side effects other than None or NoneOnDryRun, and that
		// refusal is reported as this target's outcome rather than silently
		// swallowed (FR-03).
		logger.Info("Dry-run rejected", "target", t.Ref.String(), "reason", err.Error())
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = "dry-run rejected: " + err.Error()
		r.forget(w, t.Ref)
		return row
	}
	r.remember(w, t.Ref, row.Hash)
	return row
}

// cacheKey scopes a cached dry-run to one request object and one target, so
// two requests naming the same namespace never share an acceptance (G-03
// permits two such objects).
func cacheKey(w *v1alpha1.WorkloadHardening, ref plan.Target) string {
	return string(w.UID) + "|" + ref.String()
}

// cached reports whether this exact change was dry-run cleanly earlier.
func (r *HardeningReconciler) cached(w *v1alpha1.WorkloadHardening, ref plan.Target, hash string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verified[cacheKey(w, ref)] == hash
}

// remember records a clean dry-run.
func (r *HardeningReconciler) remember(w *v1alpha1.WorkloadHardening, ref plan.Target, hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verified == nil {
		r.verified = map[string]string{}
	}
	r.verified[cacheKey(w, ref)] = hash
}

// forget drops a cached acceptance, so a target whose dry-run failed is
// re-sent on the next pass rather than sitting behind a stale acceptance.
func (r *HardeningReconciler) forget(w *v1alpha1.WorkloadHardening, ref plan.Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.verified, cacheKey(w, ref))
}

// apply is written in Task 8. The stub keeps Task 7 compiling; it must be
// replaced, not kept.
func (r *HardeningReconciler) apply(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	body []byte,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	row.Outcome = v1alpha1.OutcomeUnapproved
	return row
}

// phaseFor and carry are written in Task 8. These stubs keep Task 7 compiling;
// they must be replaced, not kept.
func phaseFor(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) (v1alpha1.Phase, string) {
	return v1alpha1.PhasePreviewed, fmt.Sprintf("%d targets would change", len(rows))
}

func carry(previous, rows []v1alpha1.TargetStatus) []v1alpha1.TargetStatus { return rows }
