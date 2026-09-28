package plan

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Coverage is what a namespace's Container-scoped LimitRanges already supply
// for the resources this tool would fill. If a LimitRange supplies a request,
// the API server injects it into every pod it admits, so writing it into the
// template would restart every pod in the namespace to change nothing (BR-06).
type Coverage struct {
	request map[corev1.ResourceName]string
	limit   map[corev1.ResourceName]string
}

// Request names the LimitRange supplying a default request for r, or "".
func (c Coverage) Request(r corev1.ResourceName) string { return c.request[r] }

// Limit names the LimitRange supplying a default limit for r, or "".
func (c Coverage) Limit(r corev1.ResourceName) string { return c.limit[r] }

// Cover summarises the Container-scoped LimitRanges of one namespace against
// the values the operator asked for. The second result is a rejection reason
// when min or max excludes a value this tool would write, and "" otherwise.
//
// That check is a correctness rule, not an optimisation: LimitRange is
// enforced at pod admission, so a template whose pods it rejects patches
// cleanly and then stalls the rollout, and the API server's dry-run against
// the workload object cannot catch it (BR-06).
//
// A namespace may hold several LimitRanges and one LimitRange several
// Container-scoped items, and the API server applies all of them — so coverage
// is their union and the bounds are their intersection.
//
// ResourceQuota is deliberately not checked. A quota constraining
// requests.<resource> requires every incoming container to request it
// explicitly, so workloads with an absent request cannot already be running in
// such a namespace; and where a LimitRange supplies the default, the rule
// above has already removed the gap (BR-06, D-15).
func Cover(items []corev1.LimitRange, requests corev1.ResourceList) (Coverage, string) {
	cov := Coverage{
		request: map[corev1.ResourceName]string{},
		limit:   map[corev1.ResourceName]string{},
	}

	// Sorted by name, so a namespace whose LimitRanges both supply the same
	// resource always names the same one and the published plan does not flap
	// between passes.
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b corev1.LimitRange) int { return strings.Compare(a.Name, b.Name) })

	names := slices.Sorted(maps.Keys(requests))

	// First pass: what is supplied. It has to finish before the bounds are
	// checked, because a value this tool will not write is not a value the
	// bounds apply to — and the LimitRange supplying it may be a different one
	// from the LimitRange setting the bound.
	for i := range sorted {
		for _, item := range containerItems(&sorted[i]) {
			for _, r := range names {
				// Both defaultRequest and default supply a request. Where
				// defaultRequest is omitted Kubernetes uses default for it,
				// and even by the plainer route a defaulted limit is copied to
				// the request when the Pod is defaulted. Reading defaultRequest
				// alone reports a gap that does not exist — BR-01's mistake,
				// one scope up (BR-06, AC-06).
				_, hasDefaultRequest := item.DefaultRequest[r]
				_, hasDefault := item.Default[r]
				if hasDefaultRequest || hasDefault {
					claim(cov.request, r, sorted[i].Name)
				}
				if hasDefault {
					claim(cov.limit, r, sorted[i].Name)
				}
			}
		}
	}

	// Second pass: the bounds, for the resources this tool would actually
	// write. maxLimitRequestRatio needs no check: it constrains limit over
	// request, and BR-01 never writes a request into a container that already
	// has a limit for that resource, so this tool cannot move the ratio.
	for i := range sorted {
		for _, item := range containerItems(&sorted[i]) {
			for _, r := range names {
				if cov.request[r] != "" {
					continue
				}
				want := requests[r]
				if lo, ok := item.Min[r]; ok && want.Cmp(lo) < 0 {
					return cov, fmt.Sprintf(
						"LimitRange %q sets a Container min of %s=%s, which excludes the requested %s",
						sorted[i].Name, r, lo.String(), want.String())
				}
				if hi, ok := item.Max[r]; ok && want.Cmp(hi) > 0 {
					return cov, fmt.Sprintf(
						"LimitRange %q sets a Container max of %s=%s, which excludes the requested %s",
						sorted[i].Name, r, hi.String(), want.String())
				}
			}
		}
	}
	return cov, ""
}

// containerItems returns the Container-scoped items of one LimitRange. Pod-
// and PVC-scoped items constrain something else and default nothing into a
// container, so they are ignored (BR-06).
func containerItems(lr *corev1.LimitRange) []corev1.LimitRangeItem {
	var out []corev1.LimitRangeItem
	for i := range lr.Spec.Limits {
		if lr.Spec.Limits[i].Type == corev1.LimitTypeContainer {
			out = append(out, lr.Spec.Limits[i])
		}
	}
	return out
}

// claim records name for r only if nothing has claimed it yet, so the
// lowest-named LimitRange wins and the report is stable.
func claim(m map[corev1.ResourceName]string, r corev1.ResourceName, name string) {
	if _, ok := m[r]; !ok {
		m[r] = name
	}
}

// buildResources decides the requests for one container.
//
// The effective request is what actually applies once Kubernetes has defaulted
// the Pod, and it is not visible in the template. A container with limits set
// and requests absent has an effective request equal to the limit; filling it
// would cut the reservation and demote the pod from Guaranteed to Burstable —
// the exact harm this feature exists to avoid, performed by the feature itself
// (BR-01, AC-03).
//
// Limits are never written. Filling requests moves a container out of the
// BestEffort class, which is the first thing evicted under node pressure; being
// wrong about a request costs scheduling position and shows up in
// `kubectl describe node` rather than in a pager. A memory limit that is too
// small kills the container after a rollout that completed green (BR-03, D-07).
func (p *Plan) buildResources(c container, policy Policy) {
	var absentLimits []string

	// Sorted, so the plan and therefore the change hash do not depend on map
	// iteration order (BR-07).
	for _, r := range slices.Sorted(maps.Keys(policy.Requests)) {
		_, hasRequest := c.Spec.Resources.Requests[r]
		_, hasLimit := c.Spec.Resources.Limits[r]

		if !hasLimit && policy.Coverage.Limit(r) == "" {
			absentLimits = append(absentLimits, string(r))
		}

		switch {
		case hasRequest:
			// Already present. Never overwrite, never lower, never correct a
			// value that is already there (BR-01, D-04).
		case hasLimit:
			p.report(c.Spec.Name, "%s request defaulted from limit; no gap, and filling it would cut the reservation (BR-01)", r)
		case policy.Coverage.Request(r) != "":
			p.report(c.Spec.Name, "%s request covered by LimitRange %q; no gap, and writing it would restart every pod to change nothing (BR-06)",
				r, policy.Coverage.Request(r))
		default:
			// A local copy: Quantity.String has a pointer receiver, so a map
			// value cannot be rendered in place.
			want := policy.Requests[r]
			p.add(c.path("resources.requests."+string(r)), want.String(), want.String())
		}
	}

	if len(absentLimits) > 0 {
		p.report(c.Spec.Name, "no %s limit; limits are never written by this tool — add a LimitRange to the namespace (BR-03, D-07)",
			strings.Join(absentLimits, " or "))
	}
}
