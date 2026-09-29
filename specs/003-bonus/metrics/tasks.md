# Metrics Endpoint — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose five Prometheus series from the controller, and assert them directly from CI rather than through a dashboard.

**Architecture:** One `pkg/metrics` package owning a **private registry** and the six collectors, imported by every reconciler and served by an `http.Server` in `main` that shuts down with the controller. A private registry rather than the default one, because a vendored library that also registers the Go collector would panic the process at init. Scraping is by pod annotation — no operator, no CRDs, no `ServiceMonitor`.

**Tech Stack:** Go 1.27.1 · `github.com/prometheus/client_golang` · kind v0.32.0. Vendored.

**Spec:** `specs/003-bonus/spec.md`, the **Metrics endpoint** section. Closes 001's G-07 and 002's G-07.

**Depends on:** the integration-tests plan's Task 1 for the workflow Task 4 adds a step to, and the refactor plan's Task 4 for the `deploy` recipe.

---

## Global Constraints

- **NFR-01 is waived for exactly one dependency: `github.com/prometheus/client_golang`.** Its transitive set comes with it and is accepted; nothing else. **After adding it, run `go mod vendor` so the local build keeps working, and commit `go.mod` and `go.sum` — `vendor/` is not tracked, so there are no vendor changes to commit.** The waiver is the spec's and does not extend to any other plan.
- **No new RBAC.** Serving metrics reads nothing from the Kubernetes API.
- **Labels are closed sets.** `resource` is one of three, `phase` one of eight. A namespace or object name must never become a label value.
- **One call site per counter.** A second `metrics.TargetPatched()` anywhere would double-count a retry.
- **Port 8080 everywhere:** the flag default, the container port, the Service, and the scrape annotation.
- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1, exactly as `go.mod` declares.
- **`vendor/` is on disk but is NOT committed.** It is listed in `.gitignore` and `git ls-files vendor` returns nothing. Two consequences, and they pull in opposite directions. Locally, `vendor/modules.txt` exists, so Go builds in vendor mode automatically — verified: `go list` resolves `k8s.io/client-go` to `./vendor/...` — and a newly added dependency makes the build fail with "inconsistent vendoring" until `go mod vendor` is re-run. In a fresh clone, which is what CI gets, there is no `vendor/` at all and modules resolve from the cache against `go.sum`. So: run `go mod vendor` locally to keep building, and commit **`go.mod` and `go.sum` only**. Never run `go mod tidy`.
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (`AGENTS.md`), enforced by `make cover`. Today's total is 93.5% and the lowest package is 91.7%. `cmd/` is wiring and is excluded.
- **`AGENTS.md` role constraint:** do not edit `specs/003-bonus/spec.md`, and do not create or modify anything else under `specs/` except this file. If the implementation needs behaviour the spec does not describe, stop and raise it rather than inventing a requirement. Every such point this plan already found is listed in "Deviations and clarifications to confirm before merging" at the end.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `refactor:`, `docs:`, `build:`, `ci:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Two conditions, ordered by how likely each is to bite.


1. **An unbounded label value.** A counter labelled by object name grows its series count with the cluster and never shrinks, which is how a metrics endpoint becomes the leak it was added to detect. Expected: `Reconcile` takes a `Phase`, not a string, so a caller cannot widen the label set by passing a message. → **Task 1, Step 2.**
2. **A metrics server that ignores `SIGTERM`.** A bare `go http.ListenAndServe` does not see the signal context, so every rollout waits out the kubelet's grace period. Expected: the process exits within a few seconds of `SIGTERM`. → **Task 3, Step 4.**

A fourth, checked and handled rather than listed: a metric registered twice panics at `init`, which is a start-up crash rather than a test failure. `TestHandlerIsReusable` at **Task 1, Step 2** covers it, and the private registry is what makes it unlikely in the first place.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `pkg/metrics/metrics.go` | **New.** A private registry, five counters and one gauge, and the handler. |
| `pkg/controller/*.go` | **Modified.** One call site per series: the patch counter in the one function that patches, queue depth from the consumer because the queue de-duplicates. |
| `cmd/main/main.go` | **Modified.** A `-metrics-addr` flag and an `http.Server` that shuts down with the controller. |
| `deploy/controller.yaml` | **Modified.** A `metrics` container port and the three `prometheus.io/*` annotations the chart's default config honours. |
| `deploy/metrics-service.yaml` | **New.** A Service for the metrics port, and nothing else. |
| `.github/workflows/ci.yml` | **Modified.** One step asserting four series by name after `make verify` has exercised them. |
| `Makefile` | **Modified.** `deploy` applies the Service. |

---

### Task 1: The metrics package (001 G-07, 002 G-07)

**Files:**
- Create: `pkg/metrics/metrics.go`
- Create: `pkg/metrics/metrics_test.go`
- Modify: `go.mod`, `go.sum` (committed); `vendor/` is regenerated locally and is not tracked

**Interfaces:**
- Consumes: nothing.
- Produces, all used by Tasks 2 and 3:
  - `metrics.Handler() http.Handler`
  - `metrics.Reconcile(resource string, phase v1alpha1.Phase)`
  - `metrics.TargetPatched()`
  - `metrics.DryRunRefused()`
  - `metrics.ApplyFailed()`
  - `metrics.QueueDepth(n int)`

- [ ] **Step 1: Add the dependency and vendor it**

```bash
go get github.com/prometheus/client_golang@latest
go mod vendor      # local only: vendor/ is gitignored, and without this the
                   # build fails with "inconsistent vendoring"
go build ./... && echo BUILD-OK
```

Expected: `BUILD-OK`. Then record what the commit actually carries — the module lines, not a vendor count:

```bash
git diff --stat go.mod go.sum
```

- [ ] **Step 2: Write the failing test**

Create `pkg/metrics/metrics_test.go`:

```go
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

// Review Focus 2: phase is a closed set and resource is one of three. Neither
// a namespace nor an object name may ever reach a label, because an unbounded
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

// Review Focus 1: the collectors live in package-level vars, so a duplicate
// registration panics at init and the controller dies before it serves. Two
// Handler() calls must be safe, and so must a second scrape.
func TestHandlerIsReusable(t *testing.T) {
	_ = body(t)
	_ = body(t)
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./pkg/metrics/ -v`
Expected: FAIL — `no required module provides package .../pkg/metrics` or `undefined: Handler`.

- [ ] **Step 4: Write the implementation**

Create `pkg/metrics/metrics.go`:

```go
// Package metrics publishes what this controller does, in the Prometheus text
// exposition format, from a registry of its own.
//
// A registry of its own rather than the default one: prometheus.DefaultRegisterer
// carries Go runtime and process collectors that a vendored library may also
// register, and a duplicate registration panics. Six series and the two
// standard collectors, named explicitly, is the whole surface.
//
// NFR-01 forbids new dependencies and is waived here. Five series in the text
// format really are forty lines of net/http and sync/atomic — and a hand-rolled
// exposition is forty lines every reviewer has to check for escaping and
// "# TYPE" ordering before trusting, against a library every scraper already
// assumes. See the Metrics endpoint section of specs/003-bonus/spec.md.
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

	// A gauge, not a counter: depth goes down as well as up. The spec calls
	// all of these counters; four of them are.
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/metrics/ -v`
Expected: all three tests PASS.

- [ ] **Step 6: Verify nothing else moved**

Run: `go build ./... && go test ./... -race && make cover`
Expected: five packages `ok`, coverage at or above 90%.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum pkg/metrics
git commit -m "feat(metrics): six series on a private registry

NFR-01 waived for prometheus/client_golang, and for nothing else. The
hand-rolled version really is forty lines of net/http and sync/atomic with no
go.mod change — and forty lines every reviewer has to check for escaping and
'# TYPE' ordering before trusting, against a library every scraper assumes.
The cost is go.mod and go.sum gaining prometheus and its transitive set.
vendor/ grows too, but locally only — it is gitignored — so what a reviewer
sees is the module lines. That is the trade being accepted.

A private registry rather than the default one: a vendored library that also
registers the Go collector would panic the process at init.

Labels are resource and phase, both closed sets. Reconcile takes a Phase
rather than a string so a caller cannot put a namespace in a label and grow
the series count with the cluster.

Closes 001's G-07 and 002's G-07 at the endpoint; the wiring is next.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Instrument the reconcilers

**Files:**
- Modify: `pkg/controller/hardening.go` (the `setStatus` call sites and `evaluate`'s tail)
- Modify: `pkg/controller/execute.go` (`preview`, `apply`)
- Modify: `pkg/controller/isolation.go` (isolation's phase transitions)
- Modify: `pkg/controller/controller.go` (`processNext`)
- Modify: `pkg/controller/hardening_test.go`

**Interfaces:**
- Consumes: every function from Task 1.
- Produces: nothing new. Task 1's functions are called from the paths that already exist.

If the refactor plan has not run, `pkg/controller/isolation.go` is still `pkg/controller/reconcile.go` and `setStatus` on the hardening reconciler is still `setHardeningStatus`. Use whichever names are in the tree.

- [ ] **Step 1: Write the failing test**

Add to `pkg/controller/hardening_test.go`:

```go
// One armed pass over one target increments the patch counter once and the
// reconcile counter once, under the phase the object reached. Counting patches
// anywhere but the one place that patches would double-count a retry.
func TestMetricsCountPatchesAndPhases(t *testing.T) {
	before := counterValue(t, "hardening_targets_patched_total")

	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if got := counterValue(t, "hardening_targets_patched_total"); got != before {
		t.Errorf("a preview incremented the patch counter: %v -> %v", before, got)
	}

	previewed := storedHardening(t, r, w)
	armed := previewed.DeepCopy()
	armed.Spec.ApprovedPlan = []string{previewed.Status.Plan[0].Hash}
	r.Dyn = hardeningDynClient(t, armed)

	if err := r.Reconcile(context.Background(), hardeningKey(armed)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := counterValue(t, "hardening_targets_patched_total"); got != before+1 {
		t.Errorf("hardening_targets_patched_total = %v, want %v after one patch", got, before+1)
	}
	if counterValue(t, `hardening_reconcile_total{phase="Applied",resource="workloadhardenings"}`) == 0 {
		t.Error("no reconcile counted under phase Applied")
	}
}
```

And the helper it needs, in the same file:

```go
// counterValue scrapes the metrics handler and reads one series by its exact
// exposition prefix. Scraping rather than reaching into the collector, so the
// test fails the same way a dashboard would.
func counterValue(t *testing.T, series string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, series+" ") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimPrefix(line, series+" "), 64)
		if err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		return v
	}
	return 0
}
```

Add `net/http`, `net/http/httptest`, `strconv` and the `pkg/metrics` import.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/controller/ -run TestMetricsCountPatchesAndPhases -v`
Expected: FAIL — `hardening_targets_patched_total = 0, want 1`.

- [ ] **Step 3: Instrument the four call sites**

In `pkg/controller/execute.go`, in `apply`, immediately after the real patch succeeds and before `row.Outcome = v1alpha1.OutcomePatched`:

```go
	metrics.TargetPatched()
```

In the same file, in both `preview` and `apply`, inside the `if err := t.patch(ctx, body, dryRun()); err != nil {` block, as the first statement:

```go
		metrics.DryRunRefused()
```

And in `apply`, inside the real patch's error block:

```go
		metrics.ApplyFailed()
```

In `pkg/controller/hardening.go`, in `evaluate`, immediately after `phase, message := phaseFor(w, rows)`:

```go
	metrics.Reconcile(v1alpha1.HardeningResource.Resource, phase)
```

And in the two early-return branches that set a phase without reaching that line — the `errors.As(err, &rej)` branch and the conversion-failure branch in `Reconcile` — add the same call with `v1alpha1.PhaseRejected`.

In `pkg/controller/isolation.go`, add one `metrics.Reconcile(v1alpha1.Resource.Resource, phase)` beside each `setStatus` call that names a phase.

Add the `pkg/metrics` import to each file.

- [ ] **Step 4: Publish queue depth**

In `pkg/controller/controller.go`, in `processNext`, immediately after `defer c.queue.Done(key)`:

```go
	// Published here rather than on Add: the queue de-duplicates, so its
	// length only means anything once, from the consumer's side.
	metrics.QueueDepth(c.queue.Len())
```

- [ ] **Step 5: Run the tests**

Run: `go test ./pkg/controller/ -run TestMetricsCountPatchesAndPhases -v`
Expected: PASS.

Run: `go test ./... -race && make cover`
Expected: five packages `ok`, coverage at or above 90%.

- [ ] **Step 6: Verify nothing counts a patch twice**

```bash
grep -rn 'metrics.TargetPatched()' pkg/
```

Expected: exactly one hit, in `execute.go`'s `apply`. A second call site anywhere — a retry path, a status writer — would double-count.

- [ ] **Step 7: Commit**

```bash
git add pkg/controller
git commit -m "feat(metrics): count patches, refusals, failures, phases and queue depth

One call site each. TargetPatched lives in the single function that patches,
so a retry cannot double-count it, and queue depth is published from the
consumer because the queue de-duplicates and its length only means something
once.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Serve it from the binary

**Files:**
- Modify: `cmd/main/main.go`

**Interfaces:**
- Consumes: `metrics.Handler()` from Task 1.
- Produces: a `-metrics-addr` flag, default `:8080`. Task 4's manifest and Service both name that port.

- [ ] **Step 1: Add the flag**

In the `flag` block in `main`:

```go
		metricsAddr = flag.String("metrics-addr", ":8080", "address for the Prometheus metrics endpoint; empty disables it")
```

- [ ] **Step 2: Serve it, and shut it down with the controller (Review Focus 3)**

After the reconcilers are built and before `c.Run(ctx)`:

```go
	// Served on its own listener, shut down with the controller. A bare
	// `go http.ListenAndServe` would ignore ctx, so every rollout would wait
	// out the kubelet's grace period instead of exiting on SIGTERM.
	if *metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		server := &http.Server{Addr: *metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			logger.Info("Serving metrics", "addr", *metricsAddr)
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error(err, "Metrics endpoint stopped")
			}
		}()
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		}()
	}
```

Add `errors` and `net/http` to the imports, plus `pkg/metrics`.

`klog.FlushAndExit` in the error paths below calls `os.Exit`, which skips deferred functions — that is fine and deliberate: those paths are already failing to start.

- [ ] **Step 3: Verify it builds and serves**

```bash
go build -o /tmp/controller ./cmd/main
/tmp/controller -kubeconfig=/dev/null -metrics-addr=:18080 &
pid=$!
for i in $(seq 1 20); do curl -sf localhost:18080/metrics >/dev/null 2>&1 && break; sleep 0.2; done
curl -s localhost:18080/metrics | grep -c '^hardening_'
kill $pid
```

Expected: a non-zero count. The controller will fail to reach a cluster with that kubeconfig and log about it; the metrics listener comes up regardless, which is the point.

- [ ] **Step 4: Verify it exits on SIGTERM (Review Focus 3)**

```bash
/tmp/controller -kubeconfig=/dev/null -metrics-addr=:18081 &
pid=$!
sleep 1
kill -TERM $pid
for i in $(seq 1 30); do kill -0 $pid 2>/dev/null || break; sleep 0.2; done
if kill -0 $pid 2>/dev/null; then echo "FAIL: still running 6s after SIGTERM"; kill -9 $pid; else echo "ok: exited on SIGTERM"; fi
```

Expected: `ok: exited on SIGTERM`.

- [ ] **Step 5: Commit**

```bash
git add cmd/main/main.go
git commit -m "feat(metrics): serve /metrics from the controller binary

-metrics-addr, default :8080, empty to disable. Its own listener and its own
Shutdown on the way out: a bare go ListenAndServe ignores the signal context,
so every rollout would wait out the kubelet's grace period instead of exiting
on SIGTERM.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Manifest, Service and a CI smoke test

**Files:**
- Modify: `deploy/controller.yaml`
- Create: `deploy/metrics-service.yaml`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the `:8080` default from Task 3.
- Produces: a `Service/k8s-workload-hardening-metrics` in `isolation-system` on port 8080, and the pod annotations any scraper needs.

- [ ] **Step 1: Add the port and the scrape annotations**

In `deploy/controller.yaml`, add to the pod template's `metadata`:

```yaml
    metadata:
      labels: {app: k8s-workload-hardening}
      annotations:
        # Scraped by annotation rather than by ServiceMonitor: the raw
        # prometheus chart ships no operator and no CRDs, and its default
        # kubernetes_sd config honours these three.
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: /metrics
```

And to the container:

```yaml
          ports:
            - name: metrics
              containerPort: 8080
              protocol: TCP
```

Leave `args` as they are — `:8080` is the flag's default.

- [ ] **Step 2: Add the Service**

Create `deploy/metrics-service.yaml`:

```yaml
# A Service for the metrics port only. The controller serves no other traffic
# and takes no requests: nothing reaches it except a scraper.
apiVersion: v1
kind: Service
metadata:
  name: k8s-workload-hardening-metrics
  namespace: isolation-system
  labels: {app: k8s-workload-hardening}
spec:
  selector: {app: k8s-workload-hardening}
  ports:
    - name: metrics
      port: 8080
      targetPort: metrics
      protocol: TCP
```

- [ ] **Step 3: Apply it from `deploy`**

In the `Makefile`'s `deploy` recipe, after the `kubectl apply -f deploy/controller.yaml` line:

```make
	kubectl apply -f deploy/metrics-service.yaml
```

- [ ] **Step 4: Add the CI smoke test**

In `.github/workflows/ci.yml`, in the `kind` job, after the step that runs `make verify`:

```yaml
      - name: Metrics endpoint
        run: |
          set -euo pipefail
          kubectl -n isolation-system rollout status deployment/k8s-workload-hardening --timeout=120s
          # Assert a known series, not a 200. A port that is bound but serving
          # an empty body looks identical to a healthy one from the manifest.
          body=$(kubectl -n isolation-system run metrics-probe \
            --rm -i --restart=Never --image=curlimages/curl:8.11.1 --quiet -- \
            -sS --max-time 10 http://k8s-workload-hardening-metrics:8080/metrics)
          for want in hardening_reconcile_total hardening_targets_patched_total \
                      hardening_dryrun_refusals_total hardening_queue_depth; do
            printf '%s' "$body" | grep -q "^# TYPE $want" || { echo "missing: $want"; exit 1; }
          done
          echo "ok: /metrics serves every declared series"
```

This runs after `make verify`, so the counters have been exercised by a real reconcile rather than read at zero.

- [ ] **Step 5: Verify the manifests parse and the selector matches**

```bash
kubectl apply --dry-run=client -f deploy/controller.yaml -f deploy/metrics-service.yaml
svc=$(awk '/^  selector:/{print $2 $3}' deploy/metrics-service.yaml)
pod=$(awk '/^      labels:/{print $2 $3; exit}' deploy/controller.yaml)
echo "service selector=$svc  pod labels=$pod"
```

Expected: both manifests validate, and the two label expressions agree on `app:k8s-workload-hardening`. A Service whose selector matches nothing returns a connection refused that reads exactly like a dead controller.

- [ ] **Step 6: Document the endpoint in the README**

Under `## Tests` (or a new `## Metrics` section if the documentation plan's
README router has landed), add:

```markdown
### Metrics

The controller serves five series on `:8080/metrics` — reconciles by resource
and phase, targets patched, dry-run refusals, apply failures, and queue depth.
Queue depth is a gauge; the rest are counters.

There is no dashboard and no Prometheus in this repository. CI scrapes the
endpoint through its Service and asserts the series by name, which is what the
endpoint is for; pointing a Prometheus at it is the operator's choice of
tooling, and the pod already carries the `prometheus.io/*` annotations a
default `kubernetes_sd` config honours.
```

- [ ] **Step 7: Commit**

```bash
git add deploy/controller.yaml deploy/metrics-service.yaml Makefile .github/workflows/ci.yml README.md
git commit -m "feat(metrics): expose the port, add a Service, and smoke it in CI

Scrape annotations rather than a ServiceMonitor: the raw prometheus chart
ships no operator and no CRDs, so the annotations its default kubernetes_sd
config already honours are the smaller of the two options.

CI asserts four series by name after make verify has exercised them, not a
200 — a port that is bound and serving an empty body looks identical to a
healthy one from the manifest.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Review Focus coverage

| # | Condition | Task, Step |
| --- | --- | --- |
| 1 | An unbounded metric label | 1 Step 2 |
| 2 | A metrics server that ignores `SIGTERM` | 3 Step 4 |
| — | A metric registered twice | 1 Step 2 (`TestHandlerIsReusable`) |

## Deviations and clarifications to confirm before merging

1. **Four counters and one gauge.** Queue depth goes down as well as up, so it is a gauge; a counter that decreases breaks `rate()` and every consumer built on it. The other four are counters. Task 1.

2. **The spec's own argument for the hand-rolled version is recorded and overruled.** Five series in the text exposition format really are `net/http`, `sync/atomic` and a `fmt.Fprintf` loop in roughly forty lines with no `go.mod` change. The user's decision, recorded in the spec, is the library — and the plan states the reason on its own terms rather than deferring: a hand-rolled exposition is forty lines every reviewer must check for escaping and `# TYPE` ordering before trusting. The cost is named in Task 1's commit: `go.mod` and `go.sum` gain prometheus and its transitive set, for six series. `vendor/` grows too, but locally only — it is gitignored, so the diff a reviewer sees is the module lines.

3. **DECLINED — a readiness probe on the new port.** The port makes one nearly free and `make deploy`'s `rollout status` would start meaning something. It is still a behaviour change to the Deployment that the spec did not ask for, in a plan about metrics. Worth doing; worth doing deliberately, somewhere else.

4. **DECLINED — a `policies_written_total` for 001.** 001's G-07 asks for an endpoint, and the isolation reconciler is covered through `hardening_reconcile_total{resource="networkisolations"}`. A separate counter is a seventh series nobody asked for.


5. **The metric names are not covered by the documentation plan's drift check.** That check greps `docs/reference.md` against `pkg/apis/v1alpha1`, and these constants live in `pkg/metrics`. Task 4's CI step asserts four of the six by name against the live endpoint, which is the stronger check anyway — it fails if the series is renamed *or* never emitted.
