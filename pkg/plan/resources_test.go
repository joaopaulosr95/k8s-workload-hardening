package plan

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AC-03, the three-row truth table of BR-01. The middle row is the one that
// matters: a container with limits set and requests absent has an effective
// request equal to the limit, so there is no gap, and filling it would cut the
// pod's reservation and demote it from Guaranteed to Burstable.
func TestEffectiveRequests(t *testing.T) {
	cases := []struct {
		name      string
		resources corev1.ResourceRequirements
		gaps      []string // paths under containers[app]
		finding   string   // substring, "" for none
	}{
		{
			name:      "nothing declared: effective request is none, so both are gaps",
			resources: corev1.ResourceRequirements{},
			gaps:      []string{"resources.requests.cpu", "resources.requests.memory"},
			finding:   "",
		},
		{
			name: "memory limit and no requests: effective memory request is 1Gi, so no memory gap",
			resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
			gaps:    []string{"resources.requests.cpu"},
			finding: "memory request defaulted from limit",
		},
		{
			name: "both limits and no requests: no gap at all",
			resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
			gaps:    nil,
			finding: "request defaulted from limit",
		},
		{
			name: "explicit requests: untouched",
			resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
			gaps:    nil,
			finding: "",
		},
		{
			name: "one explicit request, one absent",
			resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
			},
			gaps:    []string{"resources.requests.cpu"},
			finding: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: c.resources}}}
			p := Build(pod, basic())

			for _, want := range []string{"resources.requests.cpu", "resources.requests.memory"} {
				full := "spec.template.spec.containers[app]." + want
				_, written := valueAt(p, full)
				shouldWrite := false
				for _, g := range c.gaps {
					if g == want {
						shouldWrite = true
					}
				}
				if written != shouldWrite {
					t.Errorf("%s written = %v, want %v", full, written, shouldWrite)
				}
			}

			reasons := findingFor(p, "app")
			if c.finding != "" && !mentions(reasons, c.finding) {
				t.Errorf("findings = %v, want one mentioning %q", reasons, c.finding)
			}
			if c.finding == "" && mentions(reasons, "defaulted from limit") {
				t.Errorf("findings = %v, want no defaulted-from-limit finding", reasons)
			}
		})
	}
}

// BR-03: an absent limit is a finding whose remedy names the mechanism that
// exists for it. Limits are never written, whatever else is true.
func TestAbsentLimitIsAFindingNotAChange(t *testing.T) {
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	p := Build(pod, basic())

	if !mentions(findingFor(p, "app"), "LimitRange") {
		t.Errorf("findings = %v, want the absent limits reported with LimitRange as the remedy", findingFor(p, "app"))
	}
	for _, c := range p.Changes {
		if strings.Contains(c.Path, "limits") {
			t.Errorf("a limit reached the plan: %s = %s", c.Path, c.Value)
		}
	}
}

// The value written is the quantity's canonical form, not the operator's
// literal spelling, because that is what the change hash is taken over. 1000m
// and 1 are one approval, not two.
func TestRequestValueIsCanonical(t *testing.T) {
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	p := Build(pod, Request{Requests: requests("1000m", "32Mi")})

	if v, _ := valueAt(p, "spec.template.spec.containers[app].resources.requests.cpu"); v != "1" {
		t.Errorf("cpu = %q, want the canonical %q", v, "1")
	}
}

// AC-01, completed: exactly the four always-on fields plus the two requests,
// at the correct levels, initContainers included, ephemeralContainers
// untouched, and no other field. Task 3 pinned the securityContext half
// against the buildResources stub; this is the whole list.
func TestBuildEmitsExactlyTheExpectedPaths(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup", Image: "busybox"}},
		Containers:     []corev1.Container{{Name: "app", Image: "nginx"}},
		EphemeralContainers: []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "busybox"},
		}},
	}

	got := paths(Build(pod, basic()))
	want := []string{
		"spec.template.spec.containers[app].resources.requests.cpu",
		"spec.template.spec.containers[app].resources.requests.memory",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.containers[app].securityContext.capabilities.drop",
		"spec.template.spec.initContainers[setup].resources.requests.cpu",
		"spec.template.spec.initContainers[setup].resources.requests.memory",
		"spec.template.spec.initContainers[setup].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop",
		"spec.template.spec.securityContext.runAsNonRoot",
		"spec.template.spec.securityContext.seccompProfile.type",
	}
	if !slicesEqual(got, want) {
		t.Errorf("paths =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, path := range got {
		if strings.Contains(path, "debug") || strings.Contains(path, "ephemeral") {
			t.Errorf("an ephemeral container reached the plan: %s", path)
		}
	}
}

// slicesEqual keeps this file free of a slices import it needs nowhere else.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// limitRange builds a LimitRange holding one Container-scoped item.
func limitRange(name string, item corev1.LimitRangeItem) corev1.LimitRange {
	item.Type = corev1.LimitTypeContainer
	return corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{item}},
	}
}

// AC-06: a defaultRequest covering memory, and a default with no
// defaultRequest, each report the memory gap as covered and omit it from the
// plan; a min/max excluding the requested values rejects the namespace.
func TestLimitRangeCoverage(t *testing.T) {
	want := requests("10m", "32Mi")

	t.Run("defaultRequest covers memory", func(t *testing.T) {
		cov, why := Cover([]corev1.LimitRange{limitRange("defaults", corev1.LimitRangeItem{
			DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		})}, want)
		if why != "" {
			t.Fatalf("rejected: %s", why)
		}
		if cov.Request(corev1.ResourceMemory) != "defaults" {
			t.Errorf("memory request coverage = %q, want defaults", cov.Request(corev1.ResourceMemory))
		}
		if cov.Request(corev1.ResourceCPU) != "" {
			t.Errorf("cpu request coverage = %q, want none", cov.Request(corev1.ResourceCPU))
		}
		assertCovered(t, cov, corev1.ResourceMemory)
	})

	t.Run("default with no defaultRequest also covers memory", func(t *testing.T) {
		// Kubernetes uses default for defaultRequest when the latter is
		// omitted, so a LimitRange declaring only default: {memory: 512Mi}
		// gives every container an effective request of 512Mi. Reading
		// defaultRequest alone reports a gap that does not exist.
		cov, why := Cover([]corev1.LimitRange{limitRange("defaults", corev1.LimitRangeItem{
			Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		})}, want)
		if why != "" {
			t.Fatalf("rejected: %s", why)
		}
		if cov.Request(corev1.ResourceMemory) != "defaults" {
			t.Errorf("memory request coverage = %q, want defaults: default supplies defaultRequest (BR-06)",
				cov.Request(corev1.ResourceMemory))
		}
		if cov.Limit(corev1.ResourceMemory) != "defaults" {
			t.Errorf("memory limit coverage = %q, want defaults", cov.Limit(corev1.ResourceMemory))
		}
		assertCovered(t, cov, corev1.ResourceMemory)
	})

	t.Run("min excluding the requested cpu rejects the namespace", func(t *testing.T) {
		_, why := Cover([]corev1.LimitRange{limitRange("bounds", corev1.LimitRangeItem{
			Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		})}, want)
		if why == "" {
			t.Fatal("accepted a min of 100m against a requested 10m")
		}
		for _, substr := range []string{"bounds", "min", "100m", "10m"} {
			if !strings.Contains(why, substr) {
				t.Errorf("reason = %q, want it to mention %q", why, substr)
			}
		}
	})

	t.Run("max excluding the requested memory rejects the namespace", func(t *testing.T) {
		_, why := Cover([]corev1.LimitRange{limitRange("bounds", corev1.LimitRangeItem{
			Max: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")},
		})}, want)
		if why == "" {
			t.Fatal("accepted a max of 16Mi against a requested 32Mi")
		}
		if !strings.Contains(why, "max") || !strings.Contains(why, "16Mi") {
			t.Errorf("reason = %q, want the bound in the message", why)
		}
	})

	t.Run("bounds on a resource the LimitRange itself supplies do not apply", func(t *testing.T) {
		// This tool will not write cpu here, so its own value is never
		// validated against the bound: the API server injects the
		// LimitRange's default instead (BR-06).
		_, why := Cover([]corev1.LimitRange{limitRange("both", corev1.LimitRangeItem{
			DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			Min:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		})}, want)
		if why != "" {
			t.Errorf("rejected: %s — the bound applies to a resource this tool does not write", why)
		}
	})

	t.Run("a Pod-scoped item defaults nothing into a container", func(t *testing.T) {
		podScoped := corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-scoped"},
			Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypePod,
				DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
				Min:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}}},
		}
		cov, why := Cover([]corev1.LimitRange{podScoped}, want)
		if why != "" {
			t.Errorf("rejected by a Pod-scoped bound: %s", why)
		}
		if cov.Request(corev1.ResourceMemory) != "" {
			t.Error("a Pod-scoped defaultRequest was treated as container coverage")
		}
	})

	t.Run("no LimitRange at all", func(t *testing.T) {
		cov, why := Cover(nil, want)
		if why != "" {
			t.Errorf("rejected with no LimitRange: %s", why)
		}
		if cov.Request(corev1.ResourceCPU) != "" || cov.Limit(corev1.ResourceMemory) != "" {
			t.Error("coverage claimed with no LimitRange")
		}
	})
}

// assertCovered checks that Build omits the covered resource from the plan and
// reports it instead — the half of AC-06 that lives in the plan rather than in
// Cover.
func assertCovered(t *testing.T, cov Coverage, r corev1.ResourceName) {
	t.Helper()
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	p := Build(pod, Request{Requests: requests("10m", "32Mi"), Coverage: cov})

	path := "spec.template.spec.containers[app].resources.requests." + string(r)
	if _, ok := valueAt(p, path); ok {
		t.Errorf("%s written although the LimitRange already supplies it", path)
	}
	if !mentions(findingFor(p, "app"), "covered by LimitRange") {
		t.Errorf("findings = %v, want the gap reported as covered", findingFor(p, "app"))
	}
}

// Review Focus 2: BR-06 says "a Container-scoped LimitRange" in the singular,
// but a namespace may hold any number and one LimitRange may hold several
// Container-scoped items. The API server applies all of them, so coverage is
// their union and the bounds are their intersection. Reading only the first
// either reports a gap that does not exist or accepts a value the API server
// will refuse at pod admission — which no dry-run catches.
func TestSeveralLimitRanges(t *testing.T) {
	want := requests("10m", "32Mi")

	t.Run("coverage is the union across LimitRanges", func(t *testing.T) {
		cov, why := Cover([]corev1.LimitRange{
			limitRange("zeta-memory", corev1.LimitRangeItem{
				Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			}),
			limitRange("alpha-cpu", corev1.LimitRangeItem{
				DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			}),
		}, want)
		if why != "" {
			t.Fatalf("rejected: %s", why)
		}
		if cov.Request(corev1.ResourceCPU) != "alpha-cpu" {
			t.Errorf("cpu coverage = %q, want alpha-cpu", cov.Request(corev1.ResourceCPU))
		}
		if cov.Request(corev1.ResourceMemory) != "zeta-memory" {
			t.Errorf("memory coverage = %q, want zeta-memory", cov.Request(corev1.ResourceMemory))
		}
	})

	t.Run("a bound in the second LimitRange still rejects", func(t *testing.T) {
		_, why := Cover([]corev1.LimitRange{
			limitRange("aaa-harmless", corev1.LimitRangeItem{
				Max: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			}),
			limitRange("zzz-strict", corev1.LimitRangeItem{
				Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}),
		}, want)
		if why == "" {
			t.Fatal("a min in the second LimitRange was not applied")
		}
		if !strings.Contains(why, "zzz-strict") {
			t.Errorf("reason = %q, want it to name the LimitRange that excluded the value", why)
		}
	})

	t.Run("several Container-scoped items in one LimitRange", func(t *testing.T) {
		multi := corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{Name: "multi"},
			Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{
				{Type: corev1.LimitTypePod, Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
				{Type: corev1.LimitTypeContainer, Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}},
				{Type: corev1.LimitTypeContainer, Max: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m")}},
			}},
		}
		cov, why := Cover([]corev1.LimitRange{multi}, want)
		if cov.Request(corev1.ResourceMemory) != "multi" {
			t.Errorf("memory coverage = %q, want multi: the second item supplies it", cov.Request(corev1.ResourceMemory))
		}
		if why == "" {
			t.Fatal("the third item's max of 5m did not reject a requested 10m")
		}
		if !strings.Contains(why, "5m") {
			t.Errorf("reason = %q, want the third item's bound", why)
		}
	})

	t.Run("coverage from one LimitRange disarms a bound in another", func(t *testing.T) {
		// The union has to be computed before the bounds are checked: the
		// LimitRange supplying the request is not the one setting the bound.
		_, why := Cover([]corev1.LimitRange{
			limitRange("aaa-bound", corev1.LimitRangeItem{
				Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}),
			limitRange("zzz-default", corev1.LimitRangeItem{
				DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			}),
		}, want)
		if why != "" {
			t.Errorf("rejected: %s — cpu is supplied by zzz-default, so this tool never writes it and the bound does not apply", why)
		}
	})
}
