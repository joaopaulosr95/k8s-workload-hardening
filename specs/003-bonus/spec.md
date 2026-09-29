---
name: Bonus
description: |
  A metrics endpoint, integration/e2e tests on kind, a naming and documentation refactor across
  001 and 002, and Diátaxis documentation for the whole project. Four scoping notes rather than
  four specs: none of them changes what this tool does to a cluster.
author: João Bastos <joaopaulosr95@gmail.com>
status: Draft
# Flip to Approved once all four items have landed. 001 and 002 are Approved;
# this one stays Draft while any of its four is unbuilt, so the status answers
# "is this spec describing shipped behaviour?" rather than "has anyone read it".
#   [x] Refactor            — specs/003-bonus/refactor/tasks.md, merged
#   [ ] Integration tests   — specs/003-bonus/integration-tests/tasks.md
#   [ ] Metrics endpoint    — specs/003-bonus/metrics/tasks.md
#   [ ] Documentation       — specs/003-bonus/docs/tasks.md
relatedResources:
  - specs/001-network-isolation/spec.md
  - specs/002-workload-hardening/spec.md
---

# Bonus

## Objective

Four items outside the two core tasks: a refactor, a metrics endpoint, integration/e2e tests on
kind, and documentation for the whole project.

## Context

None of the four is a feature. They change how existing work is named, packaged, run or
explained, so each is scoped in a paragraph rather than given the apparatus of a spec it does
not need. The two core tasks' specs stay the authority on what this tool does to a cluster.

## Refactor

Isolation was built under the assumption nothing else would exist in the project, so its types
took the unqualified names and hardening got the prefixed ones. The result is four collisions:

| Collision                                                                                  | Where                                     |
| ------------------------------------------------------------------------------------------ | ----------------------------------------- |
| `v1alpha1.Spec` / `Status` (isolation, unqualified) vs `HardeningSpec` / `HardeningStatus` | `types.go:58,71` vs `hardening.go:78,127` |
| `plan.Finding` vs `v1alpha1.Finding`                                                       | `plan.go:68`, `hardening.go:117`          |
| `Reconciler` vs `HardeningReconciler`                                                      | `reconcile.go:29`, `targets.go:33`        |
| **`plan.Policy` vs package `pkg/policy`** — unrelated things sharing a word                | `plan.go:83`, `pkg/policy`                |

Fix direction: make the **001** side explicit rather than the 002 side implicit.
`Spec`→`IsolationSpec`, `Status`→`IsolationStatus`, `Reconciler`→`IsolationReconciler`,
`types.go`→`isolation.go`, `reconcile.go`→`isolation.go`. `setHardeningStatus`→`setStatus`
comes free once the receivers are distinct. Rename `plan.Policy`→`plan.Request` and leave the
package names alone: the `pkg/policy` / `pkg/plan` asymmetry is cosmetic next to a type and a
package that mean different things by the same word.

No behaviour changes, so the existing tests are the proof. Anything that needs a test edit
beyond a rename is a finding, not a refactor.

**Build and manifests.** The same asymmetry outside Go. `make verify` runs isolation while
`make verify-hardening` runs hardening; likewise `samples` / `samples-hardening` and
`verify-crd` / `verify-crd-hardening`. Rename the 001 side to `verify-isolation` and
`samples-isolation`, and keep a `verify` that runs both — which is what the CI job in the
integration-tests section needs anyway.

`deploy` restarts `deployment/network-isolation` in namespace `isolation-system`: one controller
process serving both CRDs, named after the first one written. Renaming it is the only item in
this whole refactor that is **not** free — it moves an RBAC subject and every script that names
it — so it is a decision rather than a rename, and skipping it costs nothing but a misleading
name.

**Docs.** The README was written for 001 and 002 was appended: `# core task 2` sits at line 54
inside `## Setup`, and lines 241–340 duplicate the structure a second time (Decisions and
Limitations at 269/306 mirroring 101/169). Merge per topic, with both features in each
section. One pass, one decision.

**Architecture.** The line counts point at one function: `HardeningReconciler.discover`,
`targets.go:145–310`. Everything else in the non-test code is proportionate. The mass is in the
tests — `hardening_test.go` is 1407 lines against a 348-line source — which is worth one pass
for table consolidation and no more.

## Metrics endpoint

About six counters are worth having from a controller that runs once per request — reconciles by
phase, targets patched, targets reverted, dry-run refusals, apply failures, queue depth — and
publishing them closes 001's G-07 and 002's G-07.

**NFR-01 is waived here, deliberately, and the endpoint is built on `prometheus/client_golang`.**
The argument NFR-01 encodes is real and this is the case where it loses: six counters in the text
exposition format are `net/http`, `sync/atomic` and a `fmt.Fprintf` loop in roughly forty lines
with no `go.mod` change, but a hand-rolled exposition is forty lines every reviewer has to read
before trusting, and escaping and `# TYPE` ordering are exactly the details a hand-rolled one gets
subtly wrong. The library is what every scraper and dashboard already assumes.

The cost is named rather than waved through: `go.sum` carries no prometheus today and the repo
vendors, so this is `go get` plus `go mod vendor`, and the dependency brings a transitive set of
its own — a visibly larger `vendor/` for six counters. That is the trade being accepted.

The controller Deployment gains a metrics container port and a Service in front of it. Neither
needs a new RBAC rule: serving metrics reads nothing from the API.

### Grafana

**The raw `prometheus` and `grafana` charts, not `kube-prometheus-stack`.** The stack installs an
operator, its CRDs, node-exporter, kube-state-metrics and a default alert set in order to scrape
six counters — more moving parts than the thing being observed, and slower to stand up than the
cluster the rest of the verification runs on. The two charts on their own are a Prometheus and a
Grafana, which is the whole requirement.

The consequence to plan for is scraping. Without the prometheus-operator's CRDs there is no
`ServiceMonitor`, so the controller is scraped through Prometheus' own `scrape_configs`: either a
`kubernetes_sd_configs` job selecting the pod by namespace and label, or the `prometheus.io/scrape`
annotations the chart's default config already honours. The annotations are the smaller of the
two and are the version to write.

Both charts are installed from a `hack/` script alongside the verification scripts, not from
checked-in dashboards. A dashboard JSON that drifts from the counters is worse than no dashboard,
and the script is what makes the result reproducible by someone who did not write it.

## Integration/e2e tests on kind

Most of this exists. `hack/verify-isolation.sh` already runs the full NetworkIsolation cycle on
a live cluster — TCP and UDP, over pod IP _and_ ClusterIP, before isolation, after enforcement
converges and after the object is deleted, plus a kubelet-readiness check that catches a CNI
enforcing ingress for non-pod sources. `hack/verify-hardening.sh` and
`hack/verify-crd-hardening.sh` cover AC-15 and AC-16, including the limits-only Deployment, the
`default`-only LimitRange, the skip annotation and the provenance annotation.

The actual gap is 002's G-06, and it is narrower than "write integration tests":

1. **`.github/workflows/` exists and is empty — there is no CI at all.** Wire `make test`,
   `make verify`, `make verify-crd`, `make verify-hardening` and `make verify-crd-hardening`
   into one workflow on a kind cluster. This is the whole of the bonus for 001 and 002.
2. **envtest** for the CEL transition rules and the structural schemas of both CRDs, which
   the scripts cover only as far as `kubectl apply` reports.

   **NFR-01 is waived a second time, for `sigs.k8s.io/controller-runtime`.** The schemas and the
   transition rules are the part of this project with no Go test at all, and they are what an
   operator's `kubectl apply` meets first. The failure that argues loudest for this is specific:
   a CEL transition rule that _errors_ rather than refuses rejects every update including the
   one that arms the object, and reads as a broken CRD rather than a wrong rule. Nothing catches
   that until something applies the right object in the right order, and a Go test that runs on
   every pull request is a better place for it than a cluster job.

   `hack/verify-crd-isolation.sh` and `hack/verify-crd-hardening.sh` stay as they are — written,
   passing, and asserting the installed-and-served path rather than the schema — and the cases
   envtest covers are ported from them rather than invented.

   The control-plane binaries are fetched by `setup-envtest`, which is a build tool run through
   `go run` at a pinned version and is **not** vendored. The risk that comes with it is named
   here because it is this item's version of a green script that tested nothing: a suite that
   skips when the binaries are absent is indistinguishable from one that passed, so CI asserts
   the suite actually ran.

Rewriting the existing scripts from a fresh test plan would rebuild working code.

## Documentation

Diátaxis splits documentation by what the reader is doing: **tutorial** (learning by doing),
**how-to** (achieving a goal), **reference** (looking something up), **explanation**
(understanding why). Its central claim is that mixing two of them in one document serves
neither, and this project is a clean demonstration — it has one mode, has it unusually well, and
has nothing of the other three.

**Explanation already exists, and is the best-written thing in the repository.** It is
`specs/`. Why leaf paths rather than the shallowest path created, why no finalizer on
`WorkloadHardening`, why a hash per target rather than per plan, why requests and never limits —
all argued, with the alternative named and refused. Nothing here rewrites any of it. A second
copy of that reasoning would drift from the one that is load-bearing, and the specs are where an
implementer already looks. The documentation links to them and stops.

**The README is currently trying to be all four at once**, which is the mechanism behind the
duplication the Refactor section describes: setup, decisions, limitations and a time log,
appended once per core task, because there was no other shape available to put them in. Merging
per topic fixes the symptom. This fixes the cause, and changes what that merge should produce: a
**router** — what this is, one path in, and four links — rather than a longer single document.
The two items are done in one pass or the README is rewritten twice.

Three files, not a directory per mode:

| Mode            | File                | Contents                                                                                                                                                                                          |
| --------------- | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Tutorial**    | `docs/tutorial.md`  | One path that works: kind up, deploy, isolate two namespaces, harden a workload, approve it, watch it roll out. No choices, no alternatives, no "if you prefer". Every command is a `make` target |
| **How-to**      | `docs/how-to.md`    | One section per goal an operator actually has. Sections, not files — each is a single task, so the document does not mix modes                                                                    |
| **Reference**   | `docs/reference.md` | The annotations, the phases and per-target outcomes, the flags, the RBAC verbs and the metric names                                                                                               |
| **Explanation** | —                   | `specs/`. Linked, never restated                                                                                                                                                                  |

The how-to sections are the answers that exist today only inside business-rule prose, where an
operator halfway through an incident will not find them: exempt a workload from hardening;
approve a subset of a plan; revert a run that broke something; hand a bypass back; widen the
blast radius deliberately; read a `Stale` row. Each is three to six commands and the one sentence
that says when it is the wrong move.

Reference is half-written already, in the CRD field descriptions, which `kubectl explain` prints
at the terminal where the question is asked. `docs/reference.md` does **not** duplicate them —
it says `kubectl explain` and covers what no CRD schema can hold: the four annotation keys and
who may write each, the phase and outcome vocabulary shared across three kinds, the controller
flags, the RBAC the tool needs and what it deliberately never asks for, and the metric names
once they exist.

**Drift is the only real risk, and it is unequal.** A stale tutorial wastes an afternoon; a
stale reference is believed. The tutorial is cheap to protect: every command in it is a `make`
target, so one loop in CI asserting each named target exists turns a rename into a failed build
rather than a confused reader. Reference gets the same treatment one level down: the
annotation keys, phase names and outcome names are Go constants, so CI greps each string in
`docs/reference.md` and fails if it is not also in `pkg/apis/v1alpha1`. That catches the class of
error that matters — a documented key that no longer exists — and costs one loop in a script.

Out of scope, and stated rather than implied: no documentation site, no generator, no versioned
docs, and no man pages. Four Markdown files in a repository this size, read on the forge that
hosts them, is the whole requirement.
