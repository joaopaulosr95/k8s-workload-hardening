package plan

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	p := Build(pod, Policy{Requests: requests("1000m", "32Mi")})

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
