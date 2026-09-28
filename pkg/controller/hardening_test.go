package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// hardening builds an unarmed request over the given namespaces.
func hardening(namespaces ...string) *v1alpha1.WorkloadHardening {
	return &v1alpha1.WorkloadHardening{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-hardening", Namespace: "isolation-system", UID: hardUID},
		Spec: v1alpha1.HardeningSpec{
			Namespaces: namespaces,
			Resources: v1alpha1.ResourcePolicy{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			}},
		},
	}
}

// hardeningDynClient builds a fake dynamic client holding w. The custom list
// kind is required: the fake cannot infer one for an unregistered CRD.
func hardeningDynClient(t *testing.T, w *v1alpha1.WorkloadHardening) *dynamicfake.FakeDynamicClient {
	t.Helper()
	u, err := v1alpha1.HardeningToUnstructured(w)
	if err != nil {
		t.Fatalf("HardeningToUnstructured: %v", err)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			v1alpha1.Resource:          v1alpha1.Kind + "List",
			v1alpha1.HardeningResource: v1alpha1.HardeningKind + "List",
		},
		u,
	)
}

// storedHardening reads the request back out of the fake dynamic client.
func storedHardening(t *testing.T, r *HardeningReconciler, w *v1alpha1.WorkloadHardening) *v1alpha1.WorkloadHardening {
	t.Helper()
	u, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(w.Namespace).Get(context.Background(), w.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the request back: %v", err)
	}
	got, err := v1alpha1.HardeningFromUnstructured(u)
	if err != nil {
		t.Fatalf("HardeningFromUnstructured: %v", err)
	}
	return got
}

func hardeningKey(w *v1alpha1.WorkloadHardening) string { return w.Namespace + "/" + w.Name }

// dryRunGuard makes the fake behave like an API server: a patch carrying
// DryRun is answered from the stored object and never applied.
//
// Verified against client-go v0.37.1: the fake clientset does NOT honour
// DryRun — it mutates its object tracker exactly as a real patch would. So
// without this reactor, AC-08's "stored objects are unchanged" would pass
// only because the assertion is checking a value the fake already clobbered.
func dryRunGuard(t *testing.T, c *fake.Clientset) {
	t.Helper()
	c.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p, ok := a.(k8stesting.PatchActionImpl)
		if !ok || !slices.Contains(p.PatchOptions.DryRun, metav1.DryRunAll) {
			return false, nil, nil
		}
		obj, err := c.Tracker().Get(a.GetResource(), a.GetNamespace(), p.Name)
		return true, obj, err
	})
}

// patchActions returns the patch actions the fake recorded, in order.
func patchActions(t *testing.T, r *HardeningReconciler) []k8stesting.PatchActionImpl {
	t.Helper()
	var out []k8stesting.PatchActionImpl
	for _, a := range r.Kube.(*fake.Clientset).Actions() {
		if p, ok := a.(k8stesting.PatchActionImpl); ok {
			out = append(out, p)
		}
	}
	return out
}

// rowFor returns the plan row for one target, or the zero value.
func rowFor(w *v1alpha1.WorkloadHardening, kind, name string) v1alpha1.TargetStatus {
	for _, row := range w.Status.Plan {
		if row.Kind == kind && row.Name == name {
			return row
		}
	}
	return v1alpha1.TargetStatus{}
}

// storedTemplate reads a Deployment's template back out of the fake.
func storedTemplate(t *testing.T, r *HardeningReconciler, namespace, name string) *appsv1.Deployment {
	t.Helper()
	d, err := r.Kube.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading %s/%s: %v", namespace, name, err)
	}
	return d
}

// AC-08: preview writes nothing. Every changed target's patch is issued with
// DryRun, the stored objects are unchanged, and the per-target plan and hashes
// appear in status.
func TestPreviewWritesNothing(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), statefulSet("tenant-a", "db"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePreviewed {
		t.Fatalf("phase = %q (%s), want Previewed", got.Status.Phase, got.Status.Message)
	}
	if len(got.Status.Plan) != 2 {
		t.Fatalf("status.plan = %+v, want two rows", got.Status.Plan)
	}
	if got.Status.LastReconcileTime == "" {
		t.Error("lastReconcileTime not set")
	}

	for _, row := range got.Status.Plan {
		if row.Outcome != v1alpha1.OutcomePlanned {
			t.Errorf("%s/%s: outcome = %q, want Planned; nothing is written while unarmed",
				row.Kind, row.Name, row.Outcome)
		}
		if len(row.Hash) != 12 {
			t.Errorf("%s/%s: hash = %q, want twelve hex digits", row.Kind, row.Name, row.Hash)
		}
		if len(row.Fields) == 0 {
			t.Errorf("%s/%s: no fields published; the operator cannot see what would change", row.Kind, row.Name)
		}
		if row.Rollout == "" {
			t.Errorf("%s/%s: no rollout mechanism published (BR-09)", row.Kind, row.Name)
		}
	}

	// Every patch was a dry-run, and there were exactly as many as there are
	// changed targets.
	patches := patchActions(t, r)
	if len(patches) != 2 {
		t.Fatalf("issued %d patches, want one dry-run per changed target", len(patches))
	}
	for _, p := range patches {
		if !slices.Contains(p.PatchOptions.DryRun, metav1.DryRunAll) {
			t.Errorf("patch of %s carried no DryRun: a preview must write nothing", p.Name)
		}
	}

	// The stored objects are unchanged. The dryRunGuard is what makes this
	// assertion mean anything.
	d := storedTemplate(t, r, "tenant-a", "api")
	if d.Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Error("a provenance annotation was written during a preview")
	}
	if d.Spec.Template.Spec.SecurityContext != nil {
		t.Error("a securityContext was written during a preview")
	}
	if c := d.Spec.Template.Spec.Containers[0]; c.SecurityContext != nil || len(c.Resources.Requests) != 0 {
		t.Error("a container was patched during a preview")
	}
}

// FR-05: validation is all or nothing across namespaces. The operator named
// two and should get both or neither.
func TestValidationIsAllOrNothing(t *testing.T) {
	cases := []struct {
		name    string
		objects []runtime.Object
		request *v1alpha1.WorkloadHardening
		wants   string
	}{
		{
			name:    "a protected namespace",
			objects: []runtime.Object{ns("tenant-a"), ns("kube-system")},
			request: hardening("tenant-a", "kube-system"),
			wants:   "protected",
		},
		{
			name:    "the controller's own namespace",
			objects: []runtime.Object{ns("tenant-a"), ns("isolation-system")},
			request: hardening("tenant-a", "isolation-system"),
			wants:   "protected",
		},
		{
			name:    "a missing namespace",
			objects: []runtime.Object{ns("tenant-a"), deployment("tenant-a", "api")},
			request: hardening("tenant-a", "tenant-b"),
			wants:   `"tenant-b" does not exist`,
		},
		{
			name: "LimitRange bounds excluding the requested values",
			objects: []runtime.Object{
				ns("tenant-a"), deployment("tenant-a", "api"),
				&corev1.LimitRange{
					ObjectMeta: metav1.ObjectMeta{Name: "bounds", Namespace: "tenant-a"},
					Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
						Type: corev1.LimitTypeContainer,
						Min:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
					}}},
				},
			},
			request: hardening("tenant-a"),
			wants:   "min",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newHardener(c.objects...)
			r.Dyn = hardeningDynClient(t, c.request)
			dryRunGuard(t, r.Kube.(*fake.Clientset))

			if err := r.Reconcile(context.Background(), hardeningKey(c.request)); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			got := storedHardening(t, r, c.request)
			if got.Status.Phase != v1alpha1.PhaseRejected {
				t.Fatalf("phase = %q (%s), want Rejected", got.Status.Phase, got.Status.Message)
			}
			if !strings.Contains(got.Status.Message, c.wants) {
				t.Errorf("message = %q, want it to mention %q", got.Status.Message, c.wants)
			}
			if len(got.Status.Plan) != 0 {
				t.Errorf("status.plan = %+v, want nothing previewed", got.Status.Plan)
			}
			// Nothing was previewed, so no namespace was touched — not even
			// the valid one.
			if n := len(patchActions(t, r)); n != 0 {
				t.Errorf("issued %d patches, want none: the whole object is rejected", n)
			}
		})
	}
}
