package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// AC-10, the unarmed half: an unchanged target is not re-dry-run on resync,
// and a changed one is. The alternative is re-running the full admission
// chain, every webhook included, for every target in up to sixteen namespaces
// on every resync, forever, on behalf of an object nobody armed (FR-03).
func TestUnarmedResyncSkipsUnchangedTargets(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if n := len(patchActions(t, r)); n != 2 {
		t.Fatalf("first pass issued %d dry-runs, want 2", n)
	}
	first := storedHardening(t, r, w)
	apiHash := rowFor(first, "Deployment", "api").Hash

	// Second pass, nothing changed: no dry-run at all. Both fakes are cleared:
	// the dynamic one still holds the first pass's status write, and the
	// assertion below is about this pass only.
	r.Kube.(*fake.Clientset).ClearActions()
	r.Dyn.(*dynamicfake.FakeDynamicClient).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("second pass issued %d dry-runs, want 0: both targets are unchanged", n)
	}
	// Status is unchanged too, so the pass issues no write of any kind.
	for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("unexpected write on an unchanged resync: %s %s", a.GetVerb(), a.GetSubresource())
		}
	}

	// Now move one target's change: give web an explicit request, so its gap
	// set shrinks and its hash moves. The other target is untouched.
	web := storedTemplate(t, r, "tenant-a", "web")
	web.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("50m"),
	}
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").Update(context.Background(), web, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("moving web's change: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("third pass: %v", err)
	}
	patches := patchActions(t, r)
	if len(patches) != 1 || patches[0].Name != "web" {
		t.Fatalf("third pass dry-ran %v, want only web", patches)
	}

	third := storedHardening(t, r, w)
	if got := rowFor(third, "Deployment", "api").Hash; got != apiHash {
		t.Errorf("api's hash moved from %s to %s although nothing about it changed", apiHash, got)
	}
	if rowFor(third, "Deployment", "web").Hash == rowFor(first, "Deployment", "web").Hash {
		t.Error("web's hash did not move although its gaps changed")
	}
}

// A dry-run refusal is this target's outcome, and the other targets are
// unaffected (FR-03, error table).
func TestDryRunRefusalIsPerTarget(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	r.Kube.(*fake.Clientset).PrependReactor("patch", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.PatchActionImpl).Name == "api" {
			return true, nil, apierrors.NewInternalError(
				errors.New(`admission webhook "mutate.example.com" does not declare sideEffects: None or NoneOnDryRun`))
		}
		return false, nil, nil
	})

	// FR-03: a refusal during a preview does not requeue. The object stays
	// Previewed, the row carries the refusal, and the periodic resync retries
	// it. Returning an error would spin the queue's backoff against a webhook
	// that may refuse permanently, on behalf of an object nobody armed.
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Errorf("Reconcile = %v, want nil: a preview refusal must not requeue (FR-03)", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePreviewed {
		t.Errorf("phase = %q, want Previewed", got.Status.Phase)
	}
	api := rowFor(got, "Deployment", "api")
	if api.Outcome != v1alpha1.OutcomeFailed {
		t.Errorf("api outcome = %q, want Failed", api.Outcome)
	}
	if !strings.Contains(api.Reason, "sideEffects") {
		t.Errorf("api reason = %q, want the API server's own message, not a swallowed one", api.Reason)
	}
	if web := rowFor(got, "Deployment", "web"); web.Outcome != v1alpha1.OutcomePlanned {
		t.Errorf("web outcome = %q, want Planned: other targets are unaffected", web.Outcome)
	}

	// A refused target is not cached, so the next pass tries again.
	r.Kube.(*fake.Clientset).ClearActions()
	_ = r.Reconcile(context.Background(), hardeningKey(w))
	var retried bool
	for _, p := range patchActions(t, r) {
		if p.Name == "api" {
			retried = true
		}
	}
	if !retried {
		t.Error("the refused target was not re-sent; a failed dry-run must not be cached as an acceptance")
	}
}

// Review Focus 4: a target with no gaps yields no patch and no API call —
// stated in FR-02 but named by no acceptance criterion. It is also what makes
// AC-13's convergence work: an already-patched target has no gaps left, so a
// retry addresses only what failed.
func TestFullyHardenedTargetIssuesNoAPICall(t *testing.T) {
	hardened := deployment("tenant-a", "already-hardened", func(d *appsv1.Deployment) {
		d.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot:   ptrTo(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}
		d.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptrTo(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
		// Limits and no requests: the effective request equals the limit, so
		// there is no resource gap either, and the pod is Guaranteed (BR-01).
		d.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	})

	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), hardened)
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches for a target with no gaps, want 0", n)
	}
	got := storedHardening(t, r, w)
	if len(got.Status.Plan) != 0 {
		t.Errorf("status.plan = %+v, want no row: there is nothing to approve", got.Status.Plan)
	}
	if got.Status.Phase != v1alpha1.PhasePreviewed {
		t.Errorf("phase = %q, want Previewed", got.Status.Phase)
	}
	// The findings still report what was seen: silence is how the lie that a
	// namespace is hardened gets told (BR-04).
	var defaulted bool
	for _, f := range got.Status.Findings {
		if strings.Contains(f.Reason, "defaulted from limit") {
			defaulted = true
		}
	}
	if !defaulted {
		t.Errorf("findings = %+v, want the defaulted-from-limit observation reported", got.Status.Findings)
	}
}

func ptrTo[T any](v T) *T { return &v }

// Review Focus 6: a stored object whose spec will not convert into the typed
// struct — a quantity the schema admits but resource.Quantity refuses, or an
// object stored before the schema tightened. Returning the conversion error
// requeues forever behind a blank status, which is indistinguishable from an
// object the controller has never seen.
func TestUnreadableSpecIsRejectedNotRetriedForever(t *testing.T) {
	broken := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.GroupName + "/" + v1alpha1.Version,
		"kind":       v1alpha1.HardeningKind,
		"metadata": map[string]any{
			"name": "tenant-hardening", "namespace": "isolation-system", "uid": hardUID,
		},
		"spec": map[string]any{
			"namespaces": []any{"tenant-a"},
			// "10mm" has no valid quantity suffix: ParseQuantity refuses it,
			// so Quantity.UnmarshalJSON fails and the whole spec will not
			// convert.
			"resources": map[string]any{"requests": map[string]any{"cpu": "10mm", "memory": "32Mi"}},
		},
	}}

	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			v1alpha1.Resource:          v1alpha1.Kind + "List",
			v1alpha1.HardeningResource: v1alpha1.HardeningKind + "List",
		},
		broken,
	)

	if err := r.Reconcile(context.Background(), "isolation-system/tenant-hardening"); err != nil {
		t.Fatalf("Reconcile returned %v; an unreadable spec must be reported, not requeued forever", err)
	}

	u, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace("isolation-system").
		Get(context.Background(), "tenant-hardening", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the object back: %v", err)
	}
	status, _ := u.Object["status"].(map[string]any)
	if status == nil {
		t.Fatal("no status written; the object is indistinguishable from one never evaluated")
	}
	if status["phase"] != string(v1alpha1.PhaseRejected) {
		t.Errorf("phase = %v, want Rejected", status["phase"])
	}
	message, _ := status["message"].(string)
	if !strings.Contains(message, "spec cannot be read") {
		t.Errorf("message = %q, want it to name the cause", message)
	}

	// Nothing was read from the cluster and nothing was written to it.
	if n := len(r.Kube.(*fake.Clientset).Actions()); n != 0 {
		t.Errorf("%d cluster calls made for an object that cannot be planned", n)
	}
}

// armed returns w with the given hashes approved, stored in the fake.
func arm(t *testing.T, r *HardeningReconciler, w *v1alpha1.WorkloadHardening, hashes ...string) *v1alpha1.WorkloadHardening {
	t.Helper()
	current := storedHardening(t, r, w)
	current.Spec.ApprovedPlan = hashes
	// The API server bumps metadata.generation on every write that changes
	// spec; the dynamic fake does not. FR-05's terminality gate compares it
	// against status.observedGeneration, so a test that left it alone could
	// never exercise a re-approval at all — the same class of gap as the fake
	// clientset ignoring DryRun. Status writes go through UpdateStatus and
	// correctly do not move it.
	current.Generation++
	u, err := v1alpha1.HardeningToUnstructured(current)
	if err != nil {
		t.Fatalf("HardeningToUnstructured: %v", err)
	}
	if _, err := r.Dyn.Resource(v1alpha1.HardeningResource).Namespace(w.Namespace).
		Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("arming the request: %v", err)
	}
	return current
}

// AC-09: an approved target is patched while an unrelated workload appearing
// in the same namespace is reported Unapproved without blocking it or the
// phase; a target whose own change moved is Stale and holds the object in
// PartiallyApplied.
func TestPerTargetApproval(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	// Preview, then approve api's hash.
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	apiHash := rowFor(storedHardening(t, r, w), "Deployment", "api").Hash
	if apiHash == "" {
		t.Fatal("no hash published for api")
	}
	arm(t, r, w, apiHash)

	// A workload nobody approved appears in the same namespace.
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").
		Create(context.Background(), deployment("tenant-a", "newcomer"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding the newcomer: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase = %q (%s), want Applied: Unapproved is not a failure", got.Status.Phase, got.Status.Message)
	}
	if api := rowFor(got, "Deployment", "api"); api.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("api outcome = %q, want Patched", api.Outcome)
	}
	newcomer := rowFor(got, "Deployment", "newcomer")
	if newcomer.Outcome != v1alpha1.OutcomeUnapproved {
		t.Errorf("newcomer outcome = %q, want Unapproved", newcomer.Outcome)
	}
	if newcomer.Hash == "" {
		t.Error("the newcomer's hash was not published; the operator cannot approve what it cannot see")
	}
	if !strings.Contains(got.Status.Message, "unapproved") {
		t.Errorf("message = %q, want the unapproved count named (FR-06)", got.Status.Message)
	}

	// The approved target was patched for real, and the unapproved one was
	// never written to at all — not even a dry-run.
	patched := storedTemplate(t, r, "tenant-a", "api")
	if patched.Spec.Template.Spec.SecurityContext == nil || patched.Spec.Template.Spec.SecurityContext.RunAsNonRoot == nil {
		t.Error("api was not patched")
	}
	for _, p := range patchActions(t, r) {
		if p.Name == "newcomer" {
			t.Error("the unapproved target was sent to the API server")
		}
	}
	untouched := storedTemplate(t, r, "tenant-a", "newcomer")
	if untouched.Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Error("the unapproved target was annotated")
	}
}

// AC-09, the Stale half: a target whose own change moved since approval is not
// patched, its new hash is published for re-approval, and the object is held
// in PartiallyApplied, which is non-terminal and re-evaluated until the
// approval is updated.
func TestStaleApprovalHoldsPartiallyApplied(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	stale := rowFor(storedHardening(t, r, w), "Deployment", "api").Hash
	arm(t, r, w, stale)

	// Someone edits the workload in a way that changes its gaps.
	d := storedTemplate(t, r, "tenant-a", "api")
	d.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("50m"),
	}
	if _, err := r.Kube.AppsV1().Deployments("tenant-a").Update(context.Background(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("editing api: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	// Stale is not retryable: recomputing produces the same refusal until a
	// human updates the approval, so spinning the backoff would be pointless.
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q, want PartiallyApplied", got.Status.Phase)
	}
	api := rowFor(got, "Deployment", "api")
	if api.Outcome != v1alpha1.OutcomeStale {
		t.Errorf("api outcome = %q, want Stale", api.Outcome)
	}
	if api.Hash == stale || api.Hash == "" {
		t.Errorf("hash = %q, want the new one published for re-approval", api.Hash)
	}
	if !strings.Contains(api.Reason, api.Hash) {
		t.Errorf("reason = %q, want it to name the hash to re-approve", api.Reason)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches for a stale target, want 0", n)
	}

	// Re-approving the new hash converges.
	arm(t, r, w, api.Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("re-approved apply: %v", err)
	}
	if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after re-approval = %q (%s), want Applied", got.Status.Phase, got.Status.Message)
	}
}

// An apply always dry-runs the patch in the same pass, cache or not: the cache
// key is the target's own change, which does not move when a webhook is
// installed, a namespace gains a Pod Security label or a LimitRange appears
// (AC-10's apply half, FR-03, NFR-02).
func TestApplyAlwaysDryRunsFirst(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	// The preview cached a clean dry-run for this exact change.
	arm(t, r, w, rowFor(storedHardening(t, r, w), "Deployment", "api").Hash)

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	patches := patchActions(t, r)
	if len(patches) != 2 {
		t.Fatalf("issued %d patches, want a dry-run then the write", len(patches))
	}
	if !slices.Contains(patches[0].PatchOptions.DryRun, metav1.DryRunAll) {
		t.Error("the first patch was not a dry-run; no write may precede a dry-run of that same patch")
	}
	if len(patches[1].PatchOptions.DryRun) != 0 {
		t.Error("the second patch was still a dry-run; nothing was written")
	}
	// Both carry the same body, so the thing verified is the thing written.
	if string(patches[0].Patch) != string(patches[1].Patch) {
		t.Error("the dry-run and the write carried different bodies")
	}
}

// AC-11 on the wire: every applied patch carries the annotation in the same
// request, recording leaf paths and the values written.
func TestProvenanceRidesInTheSameRequest(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	arm(t, r, w, rowFor(storedHardening(t, r, w), "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	for _, p := range patchActions(t, r) {
		if !strings.Contains(string(p.Patch), v1alpha1.FilledAnnotation) {
			t.Errorf("a patch of %s carried no provenance annotation:\n%s", p.Name, p.Patch)
		}
	}

	filled := storedTemplate(t, r, "tenant-a", "api").Annotations[v1alpha1.FilledAnnotation]
	if filled == "" {
		t.Fatal("no provenance annotation on the patched target")
	}
	for _, want := range []string{
		"spec.template.spec.securityContext.runAsNonRoot=true",
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault",
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation=false",
		"spec.template.spec.containers[app].securityContext.capabilities.drop=[ALL]",
		"spec.template.spec.containers[app].resources.requests.cpu=10m",
		"spec.template.spec.containers[app].resources.requests.memory=32Mi",
	} {
		if !strings.Contains(filled, want) {
			t.Errorf("annotation missing %q:\n%s", want, filled)
		}
	}
	// Leaf paths, so an undo removes exactly what was added rather than a
	// block a human may have written afterwards (BR-08).
	for _, line := range strings.Split(filled, "\n") {
		path, _, _ := strings.Cut(line, "=")
		if path == "spec.template.spec.containers[app].resources" || path == "spec.template.spec.securityContext" {
			t.Errorf("a non-leaf path reached the annotation: %q", line)
		}
	}
}

// AC-13: partial failure keeps what succeeded, reports PartiallyApplied, and
// converges on retry without re-patching what already landed.
//
// Never roll back: a half-hardened namespace is not improved by un-hardening
// the half that worked (FR-04).
func TestPartialFailureKeepsWhatSucceededAndConverges(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)
	arm(t, r, w,
		rowFor(previewed, "Deployment", "api").Hash,
		rowFor(previewed, "Deployment", "web").Hash)

	// web's real write fails; its dry-run does not, so the pass gets as far as
	// attempting the write and then has to keep api's success.
	failing := true
	r.Kube.(*fake.Clientset).PrependReactor("patch", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p := a.(k8stesting.PatchActionImpl)
		if failing && p.Name == "web" && len(p.PatchOptions.DryRun) == 0 {
			return true, nil, apierrors.NewInternalError(errors.New("etcd is unhappy"))
		}
		return false, nil, nil
	})

	err := r.Reconcile(context.Background(), hardeningKey(w))
	if err == nil {
		t.Error("a failed patch must return an error so the key is requeued")
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q (%s), want PartiallyApplied", got.Status.Phase, got.Status.Message)
	}
	if api := rowFor(got, "Deployment", "api"); api.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("api outcome = %q, want Patched: what succeeded is kept", api.Outcome)
	}
	if web := rowFor(got, "Deployment", "web"); web.Outcome != v1alpha1.OutcomeFailed {
		t.Errorf("web outcome = %q, want Failed", web.Outcome)
	}
	if storedTemplate(t, r, "tenant-a", "api").Annotations[v1alpha1.FilledAnnotation] == "" {
		t.Error("api's patch was rolled back")
	}

	// The retry recomputes. api has no gaps left, so it is not re-patched;
	// only web is addressed.
	failing = false
	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("retry: %v", err)
	}

	for _, p := range patchActions(t, r) {
		if p.Name == "api" {
			t.Error("api was patched again; an already-patched target has no gaps left (BR-01)")
		}
	}

	converged := storedHardening(t, r, w)
	if converged.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after retry = %q (%s), want Applied", converged.Status.Phase, converged.Status.Message)
	}
	// Both are still reported: an Applied object whose plan is empty tells an
	// operator nothing about what it did.
	for _, name := range []string{"api", "web"} {
		if row := rowFor(converged, "Deployment", name); row.Outcome != v1alpha1.OutcomePatched {
			t.Errorf("%s outcome after convergence = %q, want Patched", name, row.Outcome)
		}
		if storedTemplate(t, r, "tenant-a", name).Annotations[v1alpha1.FilledAnnotation] == "" {
			t.Errorf("%s carries no provenance annotation", name)
		}
	}
}

// AC-14: an Applied object issues no API calls on resync, including one that
// reached Applied with targets left Unapproved; a Rejected one is re-evaluated
// and reaches Previewed once the cause clears.
func TestTerminalAndRecoveringPhases(t *testing.T) {
	t.Run("Applied is terminal, including with unapproved targets", func(t *testing.T) {
		w := hardening("tenant-a")
		r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
		r.Dyn = hardeningDynClient(t, w)
		dryRunGuard(t, r.Kube.(*fake.Clientset))

		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("preview: %v", err)
		}
		// Approve only one of the two.
		arm(t, r, w, rowFor(storedHardening(t, r, w), "Deployment", "api").Hash)
		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("apply: %v", err)
		}
		applied := storedHardening(t, r, w)
		if applied.Status.Phase != v1alpha1.PhaseApplied {
			t.Fatalf("phase = %q (%s), want Applied", applied.Status.Phase, applied.Status.Message)
		}
		if rowFor(applied, "Deployment", "web").Outcome != v1alpha1.OutcomeUnapproved {
			t.Fatal("web should have been left Unapproved")
		}

		r.Kube.(*fake.Clientset).ClearActions()
		r.Dyn.(*dynamicfake.FakeDynamicClient).ClearActions()
		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("resync: %v", err)
		}

		// No workload call of any kind: not a list, not a get, not a patch.
		if n := len(r.Kube.(*fake.Clientset).Actions()); n != 0 {
			t.Errorf("%d cluster calls on an Applied resync, want 0", n)
		}
		// The request itself is read — that is unavoidable — but never written.
		for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
			switch a.GetVerb() {
			case "get", "list", "watch":
			default:
				t.Errorf("unexpected write on an Applied resync: %s %s", a.GetVerb(), a.GetSubresource())
			}
		}
	})

	t.Run("Rejected recovers once the cause clears", func(t *testing.T) {
		w := hardening("tenant-a", "tenant-b")
		r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
		r.Dyn = hardeningDynClient(t, w)
		dryRunGuard(t, r.Kube.(*fake.Clientset))

		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseRejected {
			t.Fatalf("phase = %q, want Rejected", got.Status.Phase)
		}

		if _, err := r.Kube.CoreV1().Namespaces().Create(context.Background(), ns("tenant-b"), metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating tenant-b: %v", err)
		}
		if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
			t.Fatalf("recovery pass: %v", err)
		}
		got := storedHardening(t, r, w)
		if got.Status.Phase != v1alpha1.PhasePreviewed {
			t.Errorf("phase = %q (%s), want Previewed: Rejected is not terminal", got.Status.Phase, got.Status.Message)
		}
		if len(got.Status.Plan) == 0 {
			t.Error("no plan published after recovery")
		}
	})
}

// Review Focus 3: an approvedPlan holding hashes that match nothing — a
// copy-paste from a stale preview, or an approval landing after someone else
// patched the targets. Every target is then Unapproved, nothing fails, and
// nothing is Stale. FR-06's literal reading makes that Applied, which is
// terminal, so the operator's approval would silently do nothing forever and
// no resync would ever look again.
func TestApprovalMatchingNothingIsNotTerminal(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	// A hash that is well-formed and belongs to nothing.
	arm(t, r, w, "deadbeef0000")

	// The preview's dry-run is already recorded; the assertion below is about
	// the apply pass, which must write nothing.
	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase == v1alpha1.PhaseApplied {
		t.Fatal("phase = Applied: a terminal phase would strand the request with the approval having done nothing")
	}
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q, want PartiallyApplied", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "none of the 1 approved hashes") {
		t.Errorf("message = %q, want it to say the approval matched nothing", got.Status.Message)
	}
	if row := rowFor(got, "Deployment", "api"); row.Outcome != v1alpha1.OutcomeUnapproved {
		t.Errorf("api outcome = %q, want Unapproved", row.Outcome)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches, want 0", n)
	}

	// Non-terminal, so correcting the approval still works.
	arm(t, r, w, rowFor(got, "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("corrected apply: %v", err)
	}
	if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after correction = %q (%s), want Applied", got.Status.Phase, got.Status.Message)
	}
}

// The half of Review Focus 3 that a count of Stale rows does not catch: one
// approval lands and another matches nothing. Folding the phase on patched >
// 0 gives Applied, which is terminal, and the second approval is never looked
// at again — so the phase folds on danglingApprovals instead.
func TestDanglingApprovalOutlivesASuccessfulPatch(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)

	// One real approval, one that belongs to nothing.
	arm(t, r, w, rowFor(previewed, "Deployment", "api").Hash, "deadbeef0000")

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if row := rowFor(got, "Deployment", "api"); row.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("api outcome = %q, want Patched: the real approval must still land", row.Outcome)
	}
	if got.Status.Phase == v1alpha1.PhaseApplied {
		t.Fatal("phase = Applied: terminal, so the approval that matched nothing is never revisited")
	}
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q (%s), want PartiallyApplied", got.Status.Phase, got.Status.Message)
	}
	if !strings.Contains(got.Status.Message, "1 of the 2 approved hashes") {
		t.Errorf("message = %q, want it to name the dangling approval", got.Status.Message)
	}

	// Dropping the bad hash settles it: api stays patched and carries forward.
	arm(t, r, w, rowFor(got, "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("corrected apply: %v", err)
	}
	if got := storedHardening(t, r, w); got.Status.Phase != v1alpha1.PhaseApplied {
		t.Errorf("phase after correction = %q (%s), want Applied", got.Status.Phase, got.Status.Message)
	}
}

// AC-18: Applied is terminal only until the approval changes. approvedPlan is
// the only mutable field in spec, so an operator extending it — FR-01's next
// batch, or the rest of a subset deliberately approved earlier (BR-07) — must
// be acted on. A resync that moves nothing still issues no API calls (AC-14).
func TestApprovalExtendedAfterApplied(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"), deployment("tenant-a", "web"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)

	// Approve a subset. BR-07 calls this the normal way to use the gate.
	arm(t, r, w, rowFor(previewed, "Deployment", "api").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	applied := storedHardening(t, r, w)
	if applied.Status.Phase != v1alpha1.PhaseApplied {
		t.Fatalf("phase = %q (%s), want Applied", applied.Status.Phase, applied.Status.Message)
	}
	if applied.Status.ObservedGeneration != applied.Generation {
		t.Fatalf("observedGeneration = %d, want %d: the gate compares these two",
			applied.Status.ObservedGeneration, applied.Generation)
	}

	// A resync moves no generation, so the object stays terminal and silent.
	before := len(r.Kube.(*fake.Clientset).Actions())
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if after := len(r.Kube.(*fake.Clientset).Actions()); after != before {
		t.Errorf("resync issued %d API calls, want 0 (AC-14)", after-before)
	}

	// Extending the approval moves generation, so the terminal phase gives way.
	patchesBefore := len(patchActions(t, r))
	arm(t, r, w, rowFor(applied, "Deployment", "api").Hash, rowFor(previewed, "Deployment", "web").Hash)
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("second batch: %v", err)
	}

	got := storedHardening(t, r, w)
	if row := rowFor(got, "Deployment", "web"); row.Outcome != v1alpha1.OutcomePatched {
		t.Errorf("web outcome = %q, want Patched: the extended approval was ignored", row.Outcome)
	}
	// api has no gaps left (BR-01), so it is not patched a second time.
	for _, a := range patchActions(t, r)[patchesBefore:] {
		if a.GetName() == "api" {
			t.Error("api was patched again; BR-01 means an already-patched target has no gaps left")
		}
	}
}

// Review Focus 5: a namespace that vanishes between validation and apply.
// Validation reads namespaces first and is all-or-nothing; the workload List
// that follows returns an empty list, not NotFound, once the namespace is
// gone. The request must not report Applied claiming it hardened a namespace
// that no longer exists.
func TestNamespaceVanishingBetweenValidationAndApply(t *testing.T) {
	w := hardening("tenant-a", "tenant-b")
	r := newHardener(
		ns("tenant-a"), deployment("tenant-a", "api"),
		ns("tenant-b"), deployment("tenant-b", "gone-soon"),
	)
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewed := storedHardening(t, r, w)
	arm(t, r, w, rowFor(previewed, "Deployment", "gone-soon").Hash)

	// tenant-b's namespace object is still there — validation passes — but
	// its contents are gone, so the List comes back empty rather than
	// erroring. That is what a namespace being reaped looks like mid-pass.
	if err := r.Kube.AppsV1().Deployments("tenant-b").
		Delete(context.Background(), "gone-soon", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting gone-soon: %v", err)
	}

	r.Kube.(*fake.Clientset).ClearActions()
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase == v1alpha1.PhaseApplied {
		t.Fatal("phase = Applied: nothing was patched, and Applied is terminal")
	}
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q (%s), want PartiallyApplied", got.Status.Phase, got.Status.Message)
	}
	// The vanished target is simply absent from the recomputed plan, not
	// reported as Failed: it was never patched and nothing went wrong.
	if row := rowFor(got, "Deployment", "gone-soon"); row.Outcome != "" {
		t.Errorf("gone-soon outcome = %q, want it absent from the recomputed plan", row.Outcome)
	}
	if n := len(patchActions(t, r)); n != 0 {
		t.Errorf("issued %d patches, want 0", n)
	}

	// And a namespace that disappears entirely rejects the whole object again,
	// because validation is all-or-nothing (FR-05).
	if err := r.Kube.CoreV1().Namespaces().Delete(context.Background(), "tenant-b", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting tenant-b: %v", err)
	}
	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("post-deletion pass: %v", err)
	}
	after := storedHardening(t, r, w)
	if after.Status.Phase != v1alpha1.PhaseRejected {
		t.Errorf("phase = %q, want Rejected once the namespace is gone", after.Status.Phase)
	}
	if !strings.Contains(after.Status.Message, "tenant-b") {
		t.Errorf("message = %q, want it to name the missing namespace", after.Status.Message)
	}
	// tenant-a was not patched either: the operator named two namespaces and
	// gets both or neither.
	if storedTemplate(t, r, "tenant-a", "api").Annotations[v1alpha1.FilledAnnotation] != "" {
		t.Error("tenant-a was patched although the object is Rejected")
	}
}

// NFR-04: a transient API failure is reported, not swallowed. An object with a
// blank phase is indistinguishable from one the controller has never seen, so
// an operator cannot tell a wedged reconcile from a controller that is not
// running. The error is still returned, so the queue retries.
func TestTransientFailureIsReportedAndRetried(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)

	boom := errors.New("etcdserver: request timed out")
	r.Kube.(*fake.Clientset).PrependReactor("list", "limitranges",
		func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, boom })

	err := r.Reconcile(context.Background(), hardeningKey(w))
	if err == nil {
		t.Fatal("Reconcile returned nil; a transient failure must be returned so the queue retries")
	}
	if !strings.Contains(err.Error(), "request timed out") {
		t.Errorf("err = %v, want the cause carried through", err)
	}

	got := storedHardening(t, r, w)
	if got.Status.Phase != v1alpha1.PhasePending {
		t.Errorf("phase = %q, want Pending: a blank phase reads as never seen", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "request timed out") {
		t.Errorf("message = %q, want the cause named", got.Status.Message)
	}
}

// NFR-02's load-bearing half: no write before a dry-run of that same patch, in
// the same pass, has been accepted. TestApplyAlwaysDryRunsFirst proves the
// happy path issues the dry-run first with the same body; this proves the
// refusal actually stops the write. Without it, reordering the two blocks — or
// logging and continuing instead of returning — would leave every other test
// green while the tool wrote a patch the API server had just refused.
func TestApplyDoesNotWriteWhenTheDryRunIsRefused(t *testing.T) {
	w := hardening("tenant-a")
	r := newHardener(ns("tenant-a"), deployment("tenant-a", "api"))
	r.Dyn = hardeningDynClient(t, w)
	dryRunGuard(t, r.Kube.(*fake.Clientset))

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err != nil {
		t.Fatalf("preview: %v", err)
	}
	hash := rowFor(storedHardening(t, r, w), "Deployment", "api").Hash
	arm(t, r, w, hash)

	// Refuse the dry-run only. A real write, if one were issued, would be
	// accepted — so the assertions below are about the tool's own ordering.
	r.Kube.(*fake.Clientset).ClearActions()
	r.Kube.(*fake.Clientset).PrependReactor("patch", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if len(a.(k8stesting.PatchActionImpl).PatchOptions.DryRun) == 0 {
			return false, nil, nil
		}
		return true, nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "apps", Kind: "Deployment"}, "api", nil)
	})

	if err := r.Reconcile(context.Background(), hardeningKey(w)); err == nil {
		t.Error("Reconcile = nil, want an error so the queue retries: this is an apply, not a preview (FR-04)")
	}

	patches := patchActions(t, r)
	if len(patches) != 1 {
		t.Fatalf("issued %d patches, want exactly 1: the refused dry-run and no write", len(patches))
	}
	if len(patches[0].PatchOptions.DryRun) == 0 {
		t.Error("the one patch issued was not a dry-run; the write went out after a refusal")
	}

	deploy, err := r.Kube.AppsV1().Deployments("tenant-a").Get(context.Background(), "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the target back: %v", err)
	}
	if got := deploy.Annotations[v1alpha1.FilledAnnotation]; got != "" {
		t.Errorf("provenance annotation = %q, want none: nothing was written", got)
	}
	if deploy.Spec.Template.Spec.SecurityContext != nil {
		t.Error("the template was patched although the dry-run refused it")
	}

	got := storedHardening(t, r, w)
	row := rowFor(got, "Deployment", "api")
	if row.Outcome != v1alpha1.OutcomeFailed {
		t.Errorf("outcome = %q, want Failed", row.Outcome)
	}
	if !strings.Contains(row.Reason, "dry-run rejected") {
		t.Errorf("reason = %q, want it to name the refused dry-run", row.Reason)
	}
	if got.Status.Phase != v1alpha1.PhasePartiallyApplied {
		t.Errorf("phase = %q, want PartiallyApplied", got.Status.Phase)
	}
}
