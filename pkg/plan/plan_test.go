package plan

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// basic is a request that fills both requests and nothing else, with no
// LimitRange in the namespace.
func basic() Request {
	return Request{Requests: requests("10m", "32Mi")}
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

	req := basic()
	req.ReadOnlyRootFilesystem = true
	on := Build(pod, req)
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
func ptr[T any](v T) *T { return &v }

// AC-02: securityContext precedence. The field you read is not the value that
// applies, and the container wins.
func TestSecurityContextPrecedence(t *testing.T) {
	t.Run("pod true with a container false is a finding, not hardened", func(t *testing.T) {
		pod := &corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true)},
			Containers:      []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(false)}}},
		}
		p := Build(pod, basic())

		// Pod level already carries the field, so there is no gap to fill
		// there; and the container's own false is never overwritten (BR-01).
		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot rewritten over a value that is already present")
		}
		if !mentions(findingFor(p, "app"), "runAsNonRoot: false") {
			t.Errorf("findings = %v, want the container's false reported", findingFor(p, "app"))
		}
	})

	t.Run("container true with nothing at pod level is left alone", func(t *testing.T) {
		pod := &corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true)}}},
		}
		p := Build(pod, basic())
		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot written where every container already declares it: the write reaches nothing")
		}
		// Narrowed to securityContext: BR-03 reports the absent limit on every
		// container, hardened or not, and that finding is Task 4's business.
		if mentions(findingFor(p, "app"), "runAsNonRoot") {
			t.Errorf("findings = %v, want no securityContext finding: the container is already hardened", findingFor(p, "app"))
		}
	})

	t.Run("one of two containers declaring it still leaves a gap", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr(true)}},
			{Name: "sidecar"},
		}}
		p := Build(pod, basic())
		if v, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); !ok || v != "true" {
			t.Error("pod-level runAsNonRoot not written where a container has no effective value")
		}
	})

	t.Run("seccompProfile follows the same rule", func(t *testing.T) {
		declared := &corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			SecurityContext: &corev1.SecurityContext{
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		}}}
		if _, ok := valueAt(Build(declared, basic()), "spec.template.spec.securityContext.seccompProfile.type"); ok {
			t.Error("pod-level seccompProfile written where every container already declares it")
		}

		unconfined := &corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			SecurityContext: &corev1.SecurityContext{
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
			},
		}}}
		p := Build(unconfined, basic())
		if _, ok := valueAt(p, "spec.template.spec.securityContext.seccompProfile.type"); ok {
			t.Error("pod-level seccompProfile written over a container that declares Unconfined; the container wins")
		}
		if !mentions(findingFor(p, "app"), "Unconfined") {
			t.Errorf("findings = %v, want the weaker profile reported", findingFor(p, "app"))
		}
	})

	t.Run("an explicit weaker container value is never overwritten", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr(true),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"NET_RAW"}},
			},
		}}}
		p := Build(pod, basic())
		for _, path := range []string{
			"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
			"spec.template.spec.containers[app].securityContext.capabilities.drop",
		} {
			if _, ok := valueAt(p, path); ok {
				t.Errorf("%s overwritten; BR-01 never corrects a value that is already present", path)
			}
		}
		reasons := findingFor(p, "app")
		if !mentions(reasons, "allowPrivilegeEscalation: true") || !mentions(reasons, "does not drop ALL") {
			t.Errorf("findings = %v, want both weaker values reported", reasons)
		}
	})

	t.Run("capabilities with only add still leaves drop a gap", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{{
			Name:            "app",
			SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}},
		}}}
		p := Build(pod, basic())
		if v, ok := valueAt(p, "spec.template.spec.containers[app].securityContext.capabilities.drop"); !ok || v != "[ALL]" {
			t.Error("capabilities.drop not written where only add is set; drop is absent and is a gap")
		}
	})
}

// AC-05: a privileged container, and an effective runAsUser of 0, each
// suppress pod-level runAsNonRoot for the whole pod and yield a finding — and
// the other three fields are still written.
//
// runAsNonRoot is a pod-level field, so a single container needing root
// poisons it for every container in the pod. Reporting that container as a
// finding, which reads as "left alone", while still imposing runAsNonRoot on
// it produces CreateContainerConfigError at the kubelet, and no dry-run
// refuses it.
func TestRootEvidenceSuppressesRunAsNonRoot(t *testing.T) {
	t.Run("a privileged container", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "agent", SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)}},
			{Name: "app"},
		}}
		p := Build(pod, basic())

		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot written beside a privileged container; this is the DaemonSet killer")
		}
		// The other three are still written.
		if _, ok := valueAt(p, "spec.template.spec.securityContext.seccompProfile.type"); !ok {
			t.Error("pod-level seccompProfile suppressed; only runAsNonRoot is (BR-02)")
		}
		for _, path := range []string{
			"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation",
			"spec.template.spec.containers[app].securityContext.capabilities.drop",
		} {
			if _, ok := valueAt(p, path); !ok {
				t.Errorf("%s suppressed; the unprivileged container is still hardened", path)
			}
		}
		// The privileged container itself is reported and untouched: the API
		// server refuses allowPrivilegeEscalation: false beside privileged: true.
		for _, path := range paths(p) {
			if strings.Contains(path, "containers[agent]") {
				t.Errorf("the privileged container was patched: %s", path)
			}
		}
		if !mentions(findingFor(p, "agent"), "privileged") {
			t.Errorf("findings = %v, want the privileged container reported", findingFor(p, "agent"))
		}
	})

	t.Run("a container-level runAsUser of 0", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "root", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(0))}},
			{Name: "app"},
		}}
		p := Build(pod, basic())

		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("pod-level runAsNonRoot written beside an effective runAsUser of 0")
		}
		if !mentions(findingFor(p, "root"), "runAsUser is 0") {
			t.Errorf("findings = %v, want the root container reported", findingFor(p, "root"))
		}
		// Unlike a privileged container, this one is not exempt from the
		// container-level fields: BR-04 names privileged containers only.
		for _, path := range []string{
			"spec.template.spec.containers[root].securityContext.allowPrivilegeEscalation",
			"spec.template.spec.containers[root].securityContext.capabilities.drop",
			"spec.template.spec.containers[root].resources.requests.cpu",
		} {
			if _, ok := valueAt(p, path); !ok {
				t.Errorf("%s suppressed; only pod-level runAsNonRoot is (BR-02)", path)
			}
		}
	})

	t.Run("a pod-level runAsUser of 0 inherited by a container", func(t *testing.T) {
		pod := &corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr(int64(0))},
			Containers:      []corev1.Container{{Name: "app"}},
		}
		if _, ok := valueAt(Build(pod, basic()), "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("the effective runAsUser is inherited from the pod and is 0; runAsNonRoot must be suppressed")
		}
	})

	t.Run("a container overriding a pod-level root to non-root is not evidence", func(t *testing.T) {
		pod := &corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr(int64(0))},
			Containers: []corev1.Container{
				{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(65532))}},
			},
		}
		if _, ok := valueAt(Build(pod, basic()), "spec.template.spec.securityContext.runAsNonRoot"); !ok {
			t.Error("no container has an effective runAsUser of 0, so runAsNonRoot must still be written")
		}
	})

	t.Run("an init container evidences root for the whole pod", func(t *testing.T) {
		pod := &corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "chown", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(0))}}},
			Containers:     []corev1.Container{{Name: "app"}},
		}
		p := Build(pod, basic())
		if _, ok := valueAt(p, "spec.template.spec.securityContext.runAsNonRoot"); ok {
			t.Error("an init container running as root still blocks the pod from starting under runAsNonRoot")
		}
		if !mentions(findingFor(p, "chown"), "runAsUser is 0") {
			t.Errorf("findings = %v, want the init container reported", findingFor(p, "chown"))
		}
	})

	t.Run("no evidence means the field is written", func(t *testing.T) {
		pod := &corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app", SecurityContext: &corev1.SecurityContext{RunAsUser: ptr(int64(65532))}},
		}}
		if v, ok := valueAt(Build(pod, basic()), "spec.template.spec.securityContext.runAsNonRoot"); !ok || v != "true" {
			t.Error("runAsNonRoot must be written where nothing evidences a need for root")
		}
	})
}
