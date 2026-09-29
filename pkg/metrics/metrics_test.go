package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// body scrapes the handler and returns the exposition text.
func body(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// Every collector is reachable through the handler, under the name a dashboard
// will hardcode. A renamed metric is a silently empty panel, so the names are
// asserted literally rather than derived.
func TestHandlerExposesEveryCollector(t *testing.T) {
	Reconcile("workloadhardenings", v1alpha1.PhaseApplied)
	TargetPatched()
	DryRunRefused()
	ApplyFailed()
	QueueDepth(7)

	got := body(t)
	for _, name := range []string{
		`hardening_reconcile_total{phase="Applied",resource="workloadhardenings"} 1`,
		"hardening_targets_patched_total 1",
		"hardening_dryrun_refusals_total 1",
		"hardening_apply_failures_total 1",
		"hardening_queue_depth 7",
	} {
		if !strings.Contains(got, name) {
			t.Errorf("missing from /metrics:\n  %s", name)
		}
	}
}

// Review Focus 1: phase is a closed set and resource is one of two. Neither a
// namespace nor an object name may ever reach a label, because an unbounded
// label value makes a counter grow without limit for the process's lifetime.
// This pins the signature: Reconcile takes a Phase, not a string.
func TestReconcileLabelsAreBounded(t *testing.T) {
	for _, phase := range []v1alpha1.Phase{
		v1alpha1.PhasePending, v1alpha1.PhaseRejected, v1alpha1.PhasePreviewed,
		v1alpha1.PhaseApplied, v1alpha1.PhasePartiallyApplied,
	} {
		Reconcile("workloadhardenings", phase)
	}

	got := body(t)
	series := strings.Count(got, "hardening_reconcile_total{")
	if series > 8 {
		t.Errorf("hardening_reconcile_total has %d series; phase x resource is bounded well below that", series)
	}
}

// The collectors live in package-level vars, so a duplicate registration
// panics at init and the controller dies before it serves. Two Handler() calls
// must be safe, and so must a second scrape.
func TestHandlerIsReusable(t *testing.T) {
	_ = body(t)
	_ = body(t)
}
