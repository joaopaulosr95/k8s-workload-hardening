// Package plan turns one workload template and one policy into the set of
// fields this tool would write into it, plus the findings it observed on the
// way. It is pure — no clients, no context, no clock — so the hard part of
// this feature is testable on its own (NFR-01, FR-02).
//
// Gaps are decided on effective values, not on what the template says, so the
// package models Kubernetes' own defaulting: pod to container precedence for
// securityContext, limits to requests for resources, and the namespace
// LimitRange for both. None of the three is observable in a template, so none
// can be skipped (BR-01).
package plan

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// templatePath is where a Deployment, StatefulSet and DaemonSet all carry the
// pod template. All three carry it at the same path, which is why all three
// are one code path (BR-04).
const templatePath = "spec.template.spec"

const (
	containersField     = "containers"
	initContainersField = "initContainers"
)

// Target identifies one workload. It is part of the change hash, so two
// workloads needing exactly the same fields never share a hash and an approval
// can never travel from one target to another (BR-07).
type Target struct {
	Namespace string
	Kind      string
	Name      string
}

func (t Target) String() string { return t.Kind + "/" + t.Namespace + "/" + t.Name }

// Change is one leaf field this tool would write.
type Change struct {
	// Path is the leaf path on the target object, with container lists keyed
	// by name rather than index:
	//
	//	spec.template.spec.securityContext.runAsNonRoot
	//	spec.template.spec.containers[app].resources.requests.cpu
	//
	// Leaf, not the shallowest path created. Recording "resources" rather than
	// "resources.requests.cpu" would make an undo delete a limits block a
	// human added afterwards, and that safety property is worth more than
	// avoiding a cosmetic empty object where a field is removed (BR-08).
	//
	// Keying by name rather than index also keeps an init container distinct
	// from a regular one and survives a reordered list.
	Path string
	// Value is Path's value rendered for the provenance annotation and the
	// change hash.
	Value string
	// JSON is the same value in the Go type the patch body needs, so the API
	// server is never handed the string "false" where it expects a bool.
	JSON any
}

// Finding is something observed and reported but never patched, with the
// reason (BR-04). Container is empty for a pod-level observation.
type Finding struct {
	Container string
	Reason    string
}

// Plan is one target's outcome. FR-02 names the return []Change; the findings
// come out of the same traversal and are the other half of BR-04, so they ride
// along rather than costing a second walk of the template.
type Plan struct {
	Changes  []Change
	Findings []Finding
}

// Policy is what the operator asked for, plus what the namespace already
// supplies.
type Policy struct {
	// Requests names the value to write into an absent request. The schema
	// requires both cpu and memory (FR-01).
	Requests corev1.ResourceList
	// ReadOnlyRootFilesystem is the one opt-in field (BR-02, D-06).
	ReadOnlyRootFilesystem bool
	// Coverage is what the namespace's LimitRanges already supply (BR-06).
	Coverage Coverage
}

// container pairs one container with the list it came from, so a path names
// the right list and an init container is never confused with a regular one.
type container struct {
	List string
	Spec *corev1.Container
}

// path returns the leaf path of field on this container.
func (c container) path(field string) string {
	return templatePath + "." + c.List + "[" + c.Spec.Name + "]." + field
}

// containers returns every container this tool may patch: both containers and
// initContainers, regular ones first, in template order. ephemeralContainers
// are excluded and are never read — they are added to running pods, not to
// templates (BR-02).
func containers(pod *corev1.PodSpec) []container {
	out := make([]container, 0, len(pod.Containers)+len(pod.InitContainers))
	for i := range pod.Containers {
		out = append(out, container{List: containersField, Spec: &pod.Containers[i]})
	}
	for i := range pod.InitContainers {
		out = append(out, container{List: initContainersField, Spec: &pod.InitContainers[i]})
	}
	return out
}

// Build returns the changes policy would make to pod and the findings it
// observed. The same function feeds the dry-run, the real patch and the
// rendered status, so a preview cannot diverge from what is applied (FR-02).
func Build(pod *corev1.PodSpec, policy Policy) Plan {
	var p Plan
	if pod == nil {
		return p
	}

	cs := containers(pod)

	// Pod level first: whether runAsNonRoot may be written at all depends on
	// every container in the pod, so the scan has to finish before the field
	// is decided (BR-02, AC-05).
	p.buildPod(pod, cs)

	for _, c := range cs {
		p.buildContainer(pod, c, policy)
	}

	// Sorted, and sorted here rather than at the call site: the change hash is
	// taken over this list, Go randomises map iteration, and an unsorted plan
	// would hash differently on most passes and invalidate every approval
	// (BR-07).
	slices.SortFunc(p.Changes, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	return p
}

// add records one change.
func (p *Plan) add(path, value string, jsonValue any) {
	p.Changes = append(p.Changes, Change{Path: path, Value: value, JSON: jsonValue})
}

// report records one finding.
func (p *Plan) report(container, format string, args ...any) {
	p.Findings = append(p.Findings, Finding{Container: container, Reason: fmt.Sprintf(format, args...)})
}

// buildPod decides the two pod-level fields.
//
// runAsNonRoot is written at pod level, so one container evidencing a need for
// root suppresses it for the entire pod. Reporting that container as a finding
// while writing the field anyway is the single most likely way this tool
// breaks a DaemonSet, and no dry-run refuses it: the failure is the kubelet's,
// at container creation (BR-02, AC-05).
//
// seccompProfile is written whatever the evidence says: it is one of the three
// fields that are still written when root is evidenced.
func (p *Plan) buildPod(pod *corev1.PodSpec, cs []container) {
	sc := pod.SecurityContext

	evidence := rootEvidence(pod, cs)
	p.Findings = append(p.Findings, evidence...)

	if len(evidence) == 0 && (sc == nil || sc.RunAsNonRoot == nil) &&
		reaches(cs, func(c *corev1.SecurityContext) bool { return c.RunAsNonRoot != nil }) {
		p.add(templatePath+".securityContext.runAsNonRoot", "true", true)
	}

	if (sc == nil || sc.SeccompProfile == nil) &&
		reaches(cs, func(c *corev1.SecurityContext) bool { return c.SeccompProfile != nil }) {
		p.add(templatePath+".securityContext.seccompProfile.type",
			string(corev1.SeccompProfileTypeRuntimeDefault),
			string(corev1.SeccompProfileTypeRuntimeDefault))
	}
}

// reaches reports whether at least one container still has no effective value
// for a pod-level field — that is, whether writing it at pod level would reach
// anything at all. A pod whose every container already declares the field has
// no gap: the container wins, so the pod-level write would change nothing
// (BR-01, AC-02). declared says whether a container's own securityContext
// carries the field.
func reaches(cs []container, declared func(*corev1.SecurityContext) bool) bool {
	for _, c := range cs {
		if c.Spec.SecurityContext == nil || !declared(c.Spec.SecurityContext) {
			return true
		}
	}
	return false
}

// rootEvidence returns a finding for every container that evidences a need for
// root. Evidence is an explicit privileged: true, or an effective runAsUser of
// 0 — the container's own if it sets one, the pod's otherwise. Both forms
// count because CNI agents, log shippers and node exporters are routinely
// privileged without ever declaring runAsUser: 0 (BR-02).
func rootEvidence(pod *corev1.PodSpec, cs []container) []Finding {
	var out []Finding
	for _, c := range cs {
		switch {
		case privileged(c.Spec):
			out = append(out, Finding{Container: c.Spec.Name, Reason: "container is explicitly privileged: " +
				"pod-level runAsNonRoot is not written, and this container's securityContext is left alone (BR-02)"})
		default:
			if uid, set := effectiveRunAsUser(pod, c.Spec); set && uid == 0 {
				out = append(out, Finding{Container: c.Spec.Name, Reason: "effective runAsUser is 0: " +
					"pod-level runAsNonRoot is not written for this pod (BR-02)"})
			}
		}
	}
	return out
}

// privileged reports whether a container is explicitly privileged.
func privileged(c *corev1.Container) bool {
	return c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged
}

// effectiveRunAsUser resolves runAsUser by pod to container precedence, the
// container winning. The second result is false when neither level sets one,
// in which case the image's own USER decides and the template is silent about
// it — which is exactly why runAsUser is never written (BR-02, D-05).
func effectiveRunAsUser(pod *corev1.PodSpec, c *corev1.Container) (int64, bool) {
	if c.SecurityContext != nil && c.SecurityContext.RunAsUser != nil {
		return *c.SecurityContext.RunAsUser, true
	}
	if pod.SecurityContext != nil && pod.SecurityContext.RunAsUser != nil {
		return *pod.SecurityContext.RunAsUser, true
	}
	return 0, false
}

// buildContainer decides the container-level fields for one container.
func (p *Plan) buildContainer(pod *corev1.PodSpec, c container, policy Policy) {
	if privileged(c.Spec) {
		// Already reported by rootEvidence, and left entirely alone. Dropping
		// a privileged container's capabilities would be theatre (BR-02), it
		// is listed under "reported as findings, never patched" (BR-04), and
		// the API server refuses allowPrivilegeEscalation: false beside
		// privileged: true outright — so writing it would fail this target's
		// whole dry-run and take its sibling containers with it.
		return
	}

	sc := c.Spec.SecurityContext

	switch {
	case sc == nil || sc.AllowPrivilegeEscalation == nil:
		p.add(c.path("securityContext.allowPrivilegeEscalation"), "false", false)
	case *sc.AllowPrivilegeEscalation:
		p.report(c.Spec.Name, "container declares allowPrivilegeEscalation: true; reported, not overruled (BR-01)")
	}

	switch {
	case sc == nil || sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0:
		// A container that also carries capabilities.add keeps it: Kubernetes
		// applies drop before add, so merging drop: [ALL] beside an existing
		// add leaves exactly the capabilities that were asked for.
		p.add(c.path("securityContext.capabilities.drop"), "[ALL]", []any{"ALL"})
	case !slices.Contains(sc.Capabilities.Drop, corev1.Capability("ALL")):
		p.report(c.Spec.Name, "capabilities.drop is already set and does not drop ALL; reported, not overruled (BR-01)")
	}

	if policy.ReadOnlyRootFilesystem {
		switch {
		case sc == nil || sc.ReadOnlyRootFilesystem == nil:
			p.add(c.path("securityContext.readOnlyRootFilesystem"), "true", true)
		case !*sc.ReadOnlyRootFilesystem:
			p.report(c.Spec.Name, "container declares readOnlyRootFilesystem: false; reported, not overruled (BR-01)")
		}
	}

	// A container overriding a pod-level field to a weaker value is reported,
	// because the container wins (FR-02, AC-02). Neither of these is a gap:
	// the value is present, and BR-01 never overwrites one.
	if sc != nil && sc.RunAsNonRoot != nil && !*sc.RunAsNonRoot {
		p.report(c.Spec.Name, "container declares runAsNonRoot: false; reported, not overruled (BR-01)")
	}
	if sc != nil && sc.SeccompProfile != nil && sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		p.report(c.Spec.Name, "container declares seccompProfile.type: %s; reported, not overruled (BR-01)", sc.SeccompProfile.Type)
	}

	p.buildResources(c, policy)
}
