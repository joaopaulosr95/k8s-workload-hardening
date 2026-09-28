package plan

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// basic is a policy that fills both requests and nothing else, with no
// LimitRange in the namespace.
func basic() Policy {
	return Policy{Requests: requests("10m", "32Mi")}
}

// paths returns the change paths, which are already sorted by Build.
func paths(p Plan) []string {
	out := make([]string, 0, len(p.Changes))
	for _, c := range p.Changes {
		out = append(out, c.Path)
	}
	return out
}

// securityPaths returns only the securityContext change paths. Task 4 adds the
// resource ones and asserts the complete list; this task owns the
// securityContext half.
func securityPaths(p Plan) []string {
	var out []string
	for _, c := range p.Changes {
		if strings.Contains(c.Path, "securityContext") {
			out = append(out, c.Path)
		}
	}
	return out
}

// valueAt returns the rendered value written at path, and whether the plan
// writes it at all.
func valueAt(p Plan, path string) (string, bool) {
	for _, c := range p.Changes {
		if c.Path == path {
			return c.Value, true
		}
	}
	return "", false
}

// findingFor returns the reasons reported against a container.
func findingFor(p Plan, container string) []string {
	var out []string
	for _, f := range p.Findings {
		if f.Container == container {
			out = append(out, f.Reason)
		}
	}
	return out
}

// mentions reports whether any of the reasons contains substr.
func mentions(reasons []string, substr string) bool {
	return slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, substr) })
}

// AC-01, the securityContext half: a template missing everything yields
// exactly the four always-on fields at the correct levels, initContainers
// included, ephemeralContainers untouched, and no other securityContext field.
// Task 4 adds the resource half and asserts the complete path list.
func TestBuildAlwaysOnFields(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup", Image: "busybox"}},
		Containers:     []corev1.Container{{Name: "app", Image: "nginx"}},
		EphemeralContainers: []corev1.EphemeralContainer{{
			EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "busybox"},
		}},
	}

	got := securityPaths(Build(pod, basic()))
	want := []string{
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.containers[app].securityContext.capabilities.drop",
		"spec.template.spec.initContainers[setup].securityContext.allowPrivilegeEscalation",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop",
		"spec.template.spec.securityContext.runAsNonRoot",
		"spec.template.spec.securityContext.seccompProfile.type",
	}
	if !slices.Equal(got, want) {
		t.Errorf("securityContext paths =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	// The paths are sorted, which is what makes the change hash stable.
	if !slices.IsSorted(paths(Build(pod, basic()))) {
		t.Error("changes are not sorted; the change hash would move between passes")
	}

	p := Build(pod, basic())
	for path, want := range map[string]string{
		"spec.template.spec.securityContext.runAsNonRoot":                             "true",
		"spec.template.spec.securityContext.seccompProfile.type":                      "RuntimeDefault",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation": "false",
		"spec.template.spec.containers[app].securityContext.capabilities.drop":        "[ALL]",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop":  "[ALL]",
	} {
		if got, ok := valueAt(p, path); !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", path, got, ok, want)
		}
	}

	// The JSON form must be the type the API server expects, not a string.
	for path, want := range map[string]any{
		"spec.template.spec.securityContext.runAsNonRoot":                             true,
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation": false,
	} {
		for _, c := range p.Changes {
			if c.Path == path && c.JSON != want {
				t.Errorf("%s JSON = %#v, want %#v", path, c.JSON, want)
			}
		}
	}

	// ephemeralContainers are added to running pods, not to templates, and are
	// never patched (BR-02, AC-01).
	for _, path := range paths(p) {
		if strings.Contains(path, "ephemeral") || strings.Contains(path, "debug") {
			t.Errorf("an ephemeral container reached the plan: %s", path)
		}
	}
}

// AC-04: readOnlyRootFilesystem appears in no plan unless requested, and in
// every eligible container when it is. No change ever names a limit.
func TestReadOnlyRootFilesystemIsOptIn(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup"}},
		Containers:     []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
	}

	off := Build(pod, basic())
	for _, path := range paths(off) {
		if strings.Contains(path, "readOnlyRootFilesystem") {
			t.Errorf("readOnlyRootFilesystem written without being requested: %s", path)
		}
	}

	policy := basic()
	policy.ReadOnlyRootFilesystem = true
	on := Build(pod, policy)
	for _, want := range []string{
		"spec.template.spec.containers[app].securityContext.readOnlyRootFilesystem",
		"spec.template.spec.containers[sidecar].securityContext.readOnlyRootFilesystem",
		"spec.template.spec.initContainers[setup].securityContext.readOnlyRootFilesystem",
	} {
		if v, ok := valueAt(on, want); !ok || v != "true" {
			t.Errorf("%s = %q (present %v), want true", want, v, ok)
		}
	}

	// No change, in either mode, ever names a limit (BR-03, AC-04).
	for _, p := range []Plan{off, on} {
		for _, path := range paths(p) {
			if strings.Contains(path, "limits") {
				t.Errorf("a limits path reached the plan: %s", path)
			}
		}
	}
}

// requests builds a ResourceList from quantity strings, panicking on a bad
// one — a test that cannot express its own input has no result to report.
func requests(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}
}
