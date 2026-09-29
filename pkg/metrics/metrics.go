// Package metrics publishes what this controller does, in the Prometheus text
// exposition format, from a registry of its own.
//
// A registry of its own rather than the default one: prometheus.DefaultRegisterer
// carries Go runtime and process collectors that a vendored library may also
// register, and a duplicate registration panics. Five series and the two
// standard collectors, named explicitly, is the whole surface.
//
// NFR-01 forbids new dependencies and is waived here, on the authority of the
// Metrics endpoint section of specs/003-bonus/spec.md. The waiver costs less
// than it reads: client_golang and its transitive set are already in go.mod,
// pulled in by controller-runtime for the envtest suite, so this promotes an
// indirect dependency to a direct one rather than adding a tree. Against that,
// the hand-rolled version is forty lines of net/http and sync/atomic that
// every reviewer has to check for escaping and "# TYPE" ordering before
// trusting, while every scraper already assumes this library.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

var registry = prometheus.NewRegistry()

var factory = promauto.With(registry)

var (
	// reconciles is labelled by resource and phase, and by nothing else.
	// A namespace or an object name here would be an unbounded label value:
	// the series count would grow with the cluster and never shrink, which is
	// the standard way a metrics endpoint becomes the leak it was added to
	// detect.
	reconciles = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "hardening_reconcile_total",
		Help: "Reconcile passes that reached a terminal decision, by resource and resulting phase.",
	}, []string{"resource", "phase"})

	targetsPatched = factory.NewCounter(prometheus.CounterOpts{
		Name: "hardening_targets_patched_total",
		Help: "Workload templates patched by WorkloadHardening.",
	})

	dryRunRefusals = factory.NewCounter(prometheus.CounterOpts{
		Name: "hardening_dryrun_refusals_total",
		Help: "Dry-run patches the API server refused.",
	})

	applyFailures = factory.NewCounter(prometheus.CounterOpts{
		Name: "hardening_apply_failures_total",
		Help: "Patches that failed after their dry-run was accepted.",
	})

	// A gauge, not a counter: depth goes down as well as up, and a counter
	// that decreases breaks rate() and every dashboard built on it. The spec
	// calls all of these counters; four of them are.
	queueDepth = factory.NewGauge(prometheus.GaugeOpts{
		Name: "hardening_queue_depth",
		Help: "Items currently in the work queue.",
	})
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Handler serves the registry. Safe to call more than once.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// Reconcile records one pass that reached a phase. The phase type rather than
// a string, so a caller cannot widen the label set by passing a message.
func Reconcile(resource string, phase v1alpha1.Phase) {
	reconciles.WithLabelValues(resource, string(phase)).Inc()
}

// TargetPatched records one workload template patched.
func TargetPatched() { targetsPatched.Inc() }

// DryRunRefused records one dry-run the API server refused.
func DryRunRefused() { dryRunRefusals.Inc() }

// ApplyFailed records one patch that failed after a clean dry-run.
func ApplyFailed() { applyFailures.Inc() }

// QueueDepth publishes the current queue length.
func QueueDepth(n int) { queueDepth.Set(float64(n)) }
