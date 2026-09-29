package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
	return requestPrefix(w.Namespace, w.Name) + string(w.UID) + "|" + ref.String()
}

// requestPrefix is the part of a cache key that identifies the request without
// its UID, so entries can be dropped for an object that is already gone — on a
// delete the UID is gone with it. The UID still scopes the key itself: a
// recreated object with the same name gets fresh entries rather than inheriting
// acceptances taken against its predecessor.
func requestPrefix(namespace, name string) string {
	return namespace + "/" + name + "|"
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

// retain drops every acceptance for this request except the targets the pass
// just saw. Nothing else removes an entry, so without it a deleted target — or
// one that dropped out of the plan because it now has no gaps — leaves a key
// behind for the lifetime of the process.
func (r *HardeningReconciler) retain(w *v1alpha1.WorkloadHardening, refs []plan.Target) {
	keep := make(map[string]bool, len(refs))
	for _, ref := range refs {
		keep[cacheKey(w, ref)] = true
	}
	prefix := requestPrefix(w.Namespace, w.Name) + string(w.UID) + "|"
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.verified {
		if strings.HasPrefix(key, prefix) && !keep[key] {
			delete(r.verified, key)
		}
	}
}

// discard drops every acceptance for a request that no longer exists.
func (r *HardeningReconciler) discard(namespace, name string) {
	prefix := requestPrefix(namespace, name)
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.verified {
		if strings.HasPrefix(key, prefix) {
			delete(r.verified, key)
		}
	}
}

// apply patches a target whose own change hash the operator approved (BR-07).
//
// Each target is decided on its own hash, never on a plan-wide one. A single
// plan-wide hash cannot converge in a live environment: any CI deploy touching
// any workload in any of up to sixteen namespaces moves it, so the operator
// re-copies the hash and is stale again before the write lands.
func (r *HardeningReconciler) apply(
	ctx context.Context,
	logger klog.Logger,
	w *v1alpha1.WorkloadHardening,
	t hardeningTarget,
	body []byte,
	row v1alpha1.TargetStatus,
) v1alpha1.TargetStatus {
	approved, wasApproved := approvedEarlier(w, t.Ref)
	switch {
	case w.Approved(row.Hash):
		// Patched below.
	case wasApproved:
		// The operator approved a hash for this target, and it no longer
		// describes it. Publish the new one for re-approval, and keep the
		// approved one so the attribution outlives this pass (BR-07).
		row.Outcome = v1alpha1.OutcomeStale
		row.ApprovedHash = approved
		row.Reason = "the approved change no longer describes this target; re-approve " + row.Hash
		logger.Info("Stale", "target", t.Ref.String(), "hash", row.Hash)
		return row
	default:
		// Appeared after the approval. Not a failure, and it never triggers a
		// retry: approving a subset is the expected use of a per-target gate
		// (FR-04, FR-06).
		row.Outcome = v1alpha1.OutcomeUnapproved
		row.Reason = "not in spec.approvedPlan; add " + row.Hash + " to approve it"
		return row
	}

	// No write before a dry-run of that same patch, in the same pass, has been
	// accepted. The cache is not consulted here: its key is the target's own
	// change, which does not move when a webhook is installed, a namespace
	// gains a Pod Security label or a LimitRange appears, so a cached
	// acceptance says nothing about whether this write will be accepted now
	// (FR-03, NFR-02).
	if err := t.patch(ctx, body, dryRun()); err != nil {
		logger.Info("Dry-run rejected", "target", t.Ref.String(), "reason", err.Error())
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = "dry-run rejected: " + err.Error()
		r.forget(w, t.Ref)
		return row
	}

	if err := t.patch(ctx, body, metav1.PatchOptions{}); err != nil {
		// Keep what succeeded elsewhere and never roll back: a half-hardened
		// namespace is not improved by un-hardening the half that worked
		// (FR-04). An API error is never assumed to have landed.
		logger.Error(err, "Patch failed", "target", t.Ref.String(), "hash", row.Hash)
		row.Outcome = v1alpha1.OutcomeFailed
		row.Reason = err.Error()
		return row
	}

	row.Outcome = v1alpha1.OutcomePatched
	logger.Info("Patched", "target", t.Ref.String(), "hash", row.Hash, "fields", row.Fields, "pods", row.Pods)
	return row
}

// approvedEarlier reports whether an earlier pass published a hash for this
// target that the operator then approved.
//
// That is what separates Stale — the operator approved a change that no longer
// exists — from Unapproved, a target that appeared after the approval. The
// previous status.plan is the only record of what was published, so it is what
// the distinction is drawn from (BR-07).
//
// With no such record — a first armed pass carrying hashes copied from
// elsewhere, or a cleared status — this returns false and the target reports
// Unapproved. BR-07 sanctions that degradation and bounds it to the label: the
// phase turns on unmatched hashes (FR-06), which need no history.
func approvedEarlier(w *v1alpha1.WorkloadHardening, ref plan.Target) (string, bool) {
	for _, row := range w.Status.Plan {
		if row.Namespace != ref.Namespace || row.Kind != ref.Kind || row.Name != ref.Name {
			continue
		}
		// On the pass that first notices the target has moved, the approved
		// hash is the one that row published. From then on the row carries it
		// explicitly, because what it publishes is the new hash.
		approved := row.Hash
		if row.ApprovedHash != "" {
			approved = row.ApprovedHash
		}
		return approved, w.Approved(approved)
	}
	return "", false
}

// rowKey orders and identifies a plan row: namespace, kind, name — the same
// deterministic order execution uses (FR-04).
func rowKey(row v1alpha1.TargetStatus) string {
	return row.Namespace + "/" + row.Kind + "/" + row.Name
}

// carry brings forward the Patched rows of the previous status for targets the
// recomputed plan no longer names.
//
// An already-patched target has no gaps left (BR-01), so it drops out of the
// plan entirely — and an Applied object whose status showed an empty plan
// would tell an operator nothing about what it did. It is also what lets
// phaseFor tell "everything approved has landed" from "the approval matched
// nothing".
func carry(previous, rows []v1alpha1.TargetStatus) []v1alpha1.TargetStatus {
	named := make(map[string]bool, len(rows))
	for _, row := range rows {
		named[rowKey(row)] = true
	}
	for _, row := range previous {
		if row.Outcome == v1alpha1.OutcomePatched && !named[rowKey(row)] {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b v1alpha1.TargetStatus) int {
		return strings.Compare(rowKey(a), rowKey(b))
	})
	return rows
}

// danglingApprovals counts the hashes in spec.approvedPlan that match no row in
// the status at all — neither a target that has gaps now, nor one this object
// patched earlier and carried forward.
//
// This is the history-free half of BR-07's gate, and it is deliberately kept
// out of the history-dependent path. Telling Stale from Unapproved needs the
// previously published status.plan and degrades to Unapproved when that is
// gone; whether an approval pointed at anything real does not need history at
// all. Folding the phase on this instead of on the Stale count means a cleared
// status costs a row's label and never lets the object go terminal with the
// operator's approval silently unapplied.
func danglingApprovals(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) int {
	present := make(map[string]bool, len(rows))
	for _, row := range rows {
		present[row.Hash] = true
	}
	var n int
	for _, h := range w.Spec.ApprovedPlan {
		if !present[h] {
			n++
		}
	}
	return n
}

// phaseFor folds the per-target outcomes into the object's phase (FR-06).
func phaseFor(w *v1alpha1.WorkloadHardening, rows []v1alpha1.TargetStatus) (v1alpha1.Phase, string) {
	var patched, failed, stale, unapproved int
	for _, row := range rows {
		switch row.Outcome {
		case v1alpha1.OutcomePatched:
			patched++
		case v1alpha1.OutcomeFailed:
			failed++
		case v1alpha1.OutcomeStale:
			stale++
		case v1alpha1.OutcomeUnapproved:
			unapproved++
		}
	}

	if !w.Armed() {
		if failed > 0 {
			return v1alpha1.PhasePreviewed, fmt.Sprintf(
				"%d targets would change; %d were refused by the dry-run", len(rows), failed)
		}
		if len(rows) == 0 {
			return v1alpha1.PhasePreviewed, "no gaps found"
		}
		return v1alpha1.PhasePreviewed, fmt.Sprintf(
			"%d targets would change; approve them by copying their hashes into spec.approvedPlan", len(rows))
	}

	dangling := danglingApprovals(w, rows)

	switch {
	case failed > 0 || stale > 0:
		// Stale is a failure of a different kind — the operator approved a
		// change that no longer exists — so it holds the object in
		// PartiallyApplied, which is non-terminal and re-evaluated until the
		// approval is updated (FR-06).
		return v1alpha1.PhasePartiallyApplied, fmt.Sprintf(
			"%d patched, %d failed, %d stale, %d unapproved", patched, failed, stale, unapproved)
	case dangling == len(w.Spec.ApprovedPlan) && patched == 0:
		// Nothing the operator approved exists. Applied is terminal, so
		// reporting it here would strand the request with the approval having
		// done nothing, forever, and no resync would ever look again.
		return v1alpha1.PhasePartiallyApplied, fmt.Sprintf(
			"none of the %d approved hashes matches a target with gaps; %d targets are unapproved",
			len(w.Spec.ApprovedPlan), unapproved)
	case dangling > 0:
		// Some landed and some pointed at nothing. This is the case a count of
		// Stale rows misses: the successful patches would otherwise carry the
		// object to Applied, which is terminal, and the approvals that matched
		// nothing would never be looked at again.
		return v1alpha1.PhasePartiallyApplied, fmt.Sprintf(
			"%d patched; %d of the %d approved hashes matches no target with gaps",
			patched, dangling, len(w.Spec.ApprovedPlan))
	default:
		// Unapproved is not a failure. An object whose approved targets all
		// patched is Applied however many targets it left alone (FR-06).
		//
		// This is the default rather than a `patched > 0` case because the
		// alternative cannot arise: reaching here means every approved hash
		// matched a row, and a row whose hash is approved leaves apply() as
		// Patched or Failed — both of which the cases above have already
		// caught. Refused targets are findings and never produce rows at all.
		return v1alpha1.PhaseApplied, fmt.Sprintf("%d patched, %d left unapproved", patched, unapproved)
	}
}
