package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/policy"
)

// dynClient builds a fake dynamic client holding iso. Both the kind and its
// list kind are registered explicitly: the fake cannot guess them for a CRD.
func dynClient(t *testing.T, iso *v1alpha1.NetworkIsolation) *dynamicfake.FakeDynamicClient {
	t.Helper()
	u, err := v1alpha1.ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(v1alpha1.GroupVersionKind, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(
		v1alpha1.GroupVersionKind.GroupVersion().WithKind(v1alpha1.Kind+"List"),
		&unstructured.UnstructuredList{})
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{v1alpha1.Resource: v1alpha1.Kind + "List"},
		u,
	)
}

// stored reads the object back out of the fake dynamic client.
func stored(t *testing.T, r *Reconciler, iso *v1alpha1.NetworkIsolation) *v1alpha1.NetworkIsolation {
	t.Helper()
	u, err := r.Dyn.Resource(v1alpha1.Resource).Namespace(iso.Namespace).Get(context.Background(), iso.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading back the object: %v", err)
	}
	got, err := v1alpha1.FromUnstructured(u)
	if err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	return got
}

func key(iso *v1alpha1.NetworkIsolation) string { return iso.Namespace + "/" + iso.Name }

// assertNoCustomResourceWrites fails if the dynamic fake recorded anything but
// reads against the NetworkIsolation.
func assertNoCustomResourceWrites(t *testing.T, r *Reconciler) {
	t.Helper()
	for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("unexpected write to the custom resource: %s %s", a.GetVerb(), a.GetSubresource())
		}
	}
}

// A clean activation: finalizer first, then both policies, then Active with the
// matched counts.
func TestActivate(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(
		ns("tenant-a"), ns("tenant-b"),
		pod("tenant-a", "gw-0", map[string]string{"app": "gateway"}, false),
	)
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseActive {
		t.Errorf("phase = %q (%s), want Active", got.Status.Phase, got.Status.Message)
	}
	if got.Status.LastReconcileTime == "" {
		t.Error("lastReconcileTime not set")
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != v1alpha1.Finalizer {
		t.Errorf("finalizers = %v", got.Finalizers)
	}

	names := policy.Names(uid)
	if len(got.Status.Peers) != 2 {
		t.Fatalf("status.peers = %v, want 2 entries", got.Status.Peers)
	}
	for i, want := range []v1alpha1.PeerStatus{{Policy: names[0], Matched: 1}, {Policy: names[1], Matched: 0}} {
		if got.Status.Peers[i] != want {
			t.Errorf("status.peers[%d] = %+v, want %+v", i, got.Status.Peers[i], want)
		}
	}
	for i, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		np, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("policy %s/%s: %v", p.ns, p.name, err)
		}
		if np.Labels[v1alpha1.OperationLabel] != uid {
			t.Errorf("policy %d missing the operation label", i)
		}
	}
}

// The finalizer must be durable before the first policy write, so a crash
// between the two never leaves an orphan policy (FR-03).
func TestFinalizerPrecedesPolicyWrites(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	finalizerPersisted := false
	r.Dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "networkisolations", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "" {
			finalizerPersisted = true
		}
		return false, nil, nil
	})
	r.Kube.(*fake.Clientset).PrependReactor("create", "networkpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !finalizerPersisted {
			t.Error("a policy was written before the finalizer was persisted")
		}
		return false, nil, nil
	})

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !finalizerPersisted {
		t.Error("finalizer never persisted")
	}
}

// AC-06: a second pass over an unchanged object writes nothing — no policy
// update, no status update. Anything else churns the API server on every
// resync and masks real changes in the audit log.
func TestReconcileIsIdempotent(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseActive {
		t.Fatalf("first pass phase = %q (%s)", got.Status.Phase, got.Status.Message)
	}
	// Zero-match selectors stay Active with counts of zero (AC-06).
	for i, p := range got.Status.Peers {
		if p.Matched != 0 {
			t.Errorf("status.peers[%d].matched = %d, want 0", i, p.Matched)
		}
	}

	r.Kube.(*fake.Clientset).ClearActions()
	r.Dyn.(*dynamicfake.FakeDynamicClient).ClearActions()

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	assertNoWrites(t, r)
	assertNoCustomResourceWrites(t, r)
}

// AC-08: one policy written, the other refused. The successful one is kept,
// the phase is Degraded, and the retry converges without ever reopening
// traffic by deleting what did land.
func TestPartialWriteDegradesAndConverges(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	names := policy.Names(uid)

	failing := true
	r.Kube.(*fake.Clientset).PrependReactor("create", "networkpolicies", func(a k8stesting.Action) (bool, runtime.Object, error) {
		np := a.(k8stesting.CreateAction).GetObject().(*networkingv1.NetworkPolicy)
		if failing && np.Name == names[1] {
			return true, nil, apierrors.NewInternalError(errors.New("etcd is unhappy"))
		}
		return false, nil, nil
	})

	if err := r.Reconcile(context.Background(), key(object)); err == nil {
		t.Error("a failed policy write must return an error so the key is requeued")
	}

	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseDegraded {
		t.Errorf("phase = %q, want Degraded", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, names[1]) {
		t.Errorf("message = %q, want it to name the failed policy", got.Status.Message)
	}
	if _, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-a").Get(context.Background(), names[0], metav1.GetOptions{}); err != nil {
		t.Errorf("the successful policy was not kept: %v", err)
	}

	// The retry converges.
	failing = false
	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got = stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseActive {
		t.Errorf("phase after retry = %q (%s), want Active", got.Status.Phase, got.Status.Message)
	}
	for i, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		if _, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{}); err != nil {
			t.Errorf("policy %d missing after convergence: %v", i, err)
		}
	}
}

// A foreign ingress policy appearing after activation must degrade, not
// reject: the owned policies are already in place and are retained, and the
// object recovers on its own when the foreign policy goes away.
func TestForeignPolicyAfterActivationDegrades(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("activation: %v", err)
	}
	names := policy.Names(uid)

	foreign := netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, nil)
	if _, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-b").Create(context.Background(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding the foreign policy: %v", err)
	}

	if err := r.Reconcile(context.Background(), key(object)); err == nil {
		t.Error("want an error so the object is retried until the foreign policy goes")
	}
	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseDegraded {
		t.Errorf("phase = %q, want Degraded (Rejected would claim nothing was written)", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "legacy") {
		t.Errorf("message = %q, want it to name the foreign policy", got.Status.Message)
	}
	if len(got.Status.Peers) != 2 {
		t.Errorf("status.peers = %v, want the owned policies still reported", got.Status.Peers)
	}
	for i, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		if _, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{}); err != nil {
			t.Errorf("owned policy %d was removed: %v", i, err)
		}
	}

	// It recovers by itself once the foreign policy is gone. The spec lists
	// this transition as untested (G-03); it is cheap to cover here.
	if err := r.Kube.NetworkingV1().NetworkPolicies("tenant-b").Delete(context.Background(), "legacy", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("removing the foreign policy: %v", err)
	}
	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if got := stored(t, r, object); got.Status.Phase != v1alpha1.PhaseActive {
		t.Errorf("phase after recovery = %q, want Active", got.Status.Phase)
	}
}

// A precondition that fails before anything was written is Rejected, and the
// finalizer is never persisted — so deleting the object has nothing to wait on.
func TestRejectionBeforeActivationWritesNothing(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a")) // tenant-b does not exist
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseRejected {
		t.Errorf("phase = %q, want Rejected", got.Status.Phase)
	}
	if len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none: nothing was written", got.Finalizers)
	}
	assertNoWrites(t, r)
}

// Review Focus 1: a namespace being deleted still answers Get and List, but
// refuses creates. That must read as Degraded with the API's own reason, not
// as a crash and not as a silent Active.
func TestTerminatingNamespaceDegrades(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	r.Kube.(*fake.Clientset).PrependReactor("create", "networkpolicies", func(a k8stesting.Action) (bool, runtime.Object, error) {
		np := a.(k8stesting.CreateAction).GetObject().(*networkingv1.NetworkPolicy)
		if np.Namespace == "tenant-b" {
			return true, nil, apierrors.NewForbidden(
				networkingv1.Resource("networkpolicies"), np.Name,
				errors.New("unable to create new content in namespace tenant-b because it is being terminated"))
		}
		return false, nil, nil
	})

	if err := r.Reconcile(context.Background(), key(object)); err == nil {
		t.Error("want an error so the key is requeued")
	}
	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseDegraded {
		t.Errorf("phase = %q, want Degraded", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "terminated") {
		t.Errorf("message = %q, want it to carry the API server's reason", got.Status.Message)
	}
}

// Review Focus 3: the finalizer write and the status write happen in one pass,
// so the second must carry the version the first returned. A conflict must
// requeue and leave the finalizer in place, not panic and not drop it.
func TestConflictOnStatusWriteRequeues(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	conflict := true
	r.Dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("update", "networkisolations", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if conflict && a.GetSubresource() == "status" {
			conflict = false
			return true, nil, apierrors.NewConflict(
				v1alpha1.Resource.GroupResource(), object.Name, errors.New("the object has been modified"))
		}
		return false, nil, nil
	})

	err := r.Reconcile(context.Background(), key(object))
	if err == nil {
		t.Fatal("a conflict must surface as an error so the key is requeued")
	}
	if !apierrors.IsConflict(err) {
		t.Errorf("err = %v, want a conflict", err)
	}
	if got := stored(t, r, object); len(got.Finalizers) != 1 {
		t.Errorf("finalizers = %v, want the finalizer retained across the conflict", got.Finalizers)
	}

	// The next pass succeeds against the stored version.
	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := stored(t, r, object); got.Status.Phase != v1alpha1.PhaseActive {
		t.Errorf("phase = %q, want Active", got.Status.Phase)
	}
}

// deleting returns an active isolation marked for deletion, as the API server
// presents it once a finalizer is holding it.
func deleting() *v1alpha1.NetworkIsolation {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	now := metav1.Now()
	object.DeletionTimestamp = &now
	object.Finalizers = []string{v1alpha1.Finalizer}
	names := policy.Names(uid)
	object.Status = v1alpha1.Status{Phase: v1alpha1.PhaseActive, Peers: []v1alpha1.PeerStatus{
		{Policy: names[0], Matched: 1}, {Policy: names[1], Matched: 1},
	}}
	return object
}

func ownedPolicy(namespace, name string) *networkingv1.NetworkPolicy {
	return netpol(namespace, name, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		map[string]string{v1alpha1.OperationLabel: uid})
}

// AC-04: both policies go, then the finalizer.
func TestCleanupRemovesPoliciesThenFinalizer(t *testing.T) {
	object := deleting()
	names := policy.Names(uid)
	r := newReconciler(ns("tenant-a"), ns("tenant-b"), ownedPolicy("tenant-a", names[0]), ownedPolicy("tenant-b", names[1]))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	for i, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		if _, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("policy %d still present (err %v)", i, err)
		}
	}
	if got := stored(t, r, object); len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want empty", got.Finalizers)
	}
}

// AC-04: policies already gone is success, not a stall.
func TestCleanupToleratesAbsentPolicies(t *testing.T) {
	object := deleting()
	r := newReconciler() // no namespaces, no policies: everything is already gone
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := stored(t, r, object); len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want empty", got.Finalizers)
	}
}

// AC-04: an API error is not evidence of absence. The finalizer stays.
func TestCleanupKeepsFinalizerOnAPIError(t *testing.T) {
	object := deleting()
	names := policy.Names(uid)
	r := newReconciler(ns("tenant-a"), ns("tenant-b"), ownedPolicy("tenant-a", names[0]), ownedPolicy("tenant-b", names[1]))
	r.Dyn = dynClient(t, object)

	r.Kube.(*fake.Clientset).PrependReactor("delete", "networkpolicies", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.DeleteAction).GetName() == names[1] {
			return true, nil, apierrors.NewInternalError(errors.New("apiserver unreachable"))
		}
		return false, nil, nil
	})

	if err := r.Reconcile(context.Background(), key(object)); err == nil {
		t.Error("want an error so deletion is retried")
	}
	if got := stored(t, r, object); len(got.Finalizers) != 1 {
		t.Errorf("finalizers = %v, want the finalizer retained", got.Finalizers)
	}
}

// AC-04: a restart mid-deletion resumes. A pass over an object with a
// deletionTimestamp must never recreate a policy, whatever the preconditions
// would say.
func TestCleanupNeverRecreates(t *testing.T) {
	object := deleting()
	names := policy.Names(uid)
	// Only one policy survived the crash; the other was already removed.
	r := newReconciler(ns("tenant-a"), ns("tenant-b"), ownedPolicy("tenant-a", names[0]))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, a := range r.Kube.(*fake.Clientset).Actions() {
		if a.GetVerb() == "create" || a.GetVerb() == "update" {
			t.Errorf("deletion pass wrote a policy: %s", a.GetVerb())
		}
	}
	for i, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		if _, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("policy %d present after cleanup (err %v)", i, err)
		}
	}
}

// A policy occupying one of our names but owned by someone else is left alone,
// even during cleanup (FR-03).
func TestCleanupLeavesForeignPolicyAlone(t *testing.T) {
	object := deleting()
	names := policy.Names(uid)
	foreign := netpol("tenant-a", names[0], []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		map[string]string{v1alpha1.OperationLabel: "some-other-uid"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"), foreign)
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-a").Get(context.Background(), names[0], metav1.GetOptions{}); err != nil {
		t.Errorf("foreign policy was deleted: %v", err)
	}
}

// Review Focus 2: an object that was Rejected never got a finalizer. Deleting
// it must be a no-op — no status write, no attempt to strip a finalizer that
// isn't there, no API calls that would fail against a missing namespace.
func TestCleanupWithoutFinalizerIsANoOp(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	now := metav1.Now()
	object.DeletionTimestamp = &now
	object.Status = v1alpha1.Status{Phase: v1alpha1.PhaseRejected, Message: `namespace "tenant-b" does not exist`}

	r := newReconciler()
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertNoWrites(t, r)
	assertNoCustomResourceWrites(t, r)
}

// FR-04 watches NetworkPolicy objects so drift is repaired. An owned policy
// edited by hand must be rewritten from the spec on the next pass.
func TestApplyPolicyRepairsDrift(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("activation: %v", err)
	}
	names := policy.Names(uid)

	// Someone widens the policy by hand: an empty peer list admits nothing,
	// but an extra allow-all rule would admit the group we are containing.
	api := r.Kube.NetworkingV1().NetworkPolicies("tenant-a")
	drifted, err := api.Get(context.Background(), names[0], metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drifted.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}} // allow from anywhere
	if _, err := api.Update(context.Background(), drifted, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("repair pass: %v", err)
	}

	repaired, err := api.Get(context.Background(), names[0], metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after repair: %v", err)
	}
	if len(repaired.Spec.Ingress) != 1 || len(repaired.Spec.Ingress[0].From) != 2 {
		t.Errorf("drift not repaired: ingress = %+v", repaired.Spec.Ingress)
	}
}

// Error table: "Policy name occupied by a foreign object — conflict reported;
// never overwritten." Validation refuses an ingress-affecting foreign policy,
// so the only way to reach this is a foreign egress-only policy holding one of
// our names.
func TestApplyPolicyRefusesForeignNameCollision(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	names := policy.Names(uid)
	squatter := netpol("tenant-a", names[0], []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		map[string]string{v1alpha1.OperationLabel: "some-other-uid"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"), squatter)
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err == nil {
		t.Error("want an error so the collision is retried")
	}

	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseDegraded {
		t.Errorf("phase = %q, want Degraded", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "does not own") {
		t.Errorf("message = %q, want it to report the collision", got.Status.Message)
	}

	// Never overwritten.
	after, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-a").Get(context.Background(), names[0], metav1.GetOptions{})
	if err != nil {
		t.Fatalf("squatter disappeared: %v", err)
	}
	if after.Labels[v1alpha1.OperationLabel] != "some-other-uid" {
		t.Errorf("squatter was adopted: labels = %v", after.Labels)
	}
	if len(after.Spec.PolicyTypes) != 1 || after.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("squatter was rewritten: %+v", after.Spec)
	}
}

// An API failure is not a precondition failure. Reporting Rejected would claim
// nothing was written and that retrying is pointless; both are wrong.
func TestAPIErrorIsRetriedNotRejected(t *testing.T) {
	for _, c := range []struct{ verb, resource string }{
		{"list", "pods"},
		{"list", "networkpolicies"},
		{"get", "namespaces"},
	} {
		t.Run(c.verb+" "+c.resource, func(t *testing.T) {
			object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
			r := newReconciler(ns("tenant-a"), ns("tenant-b"))
			r.Dyn = dynClient(t, object)
			r.Kube.(*fake.Clientset).PrependReactor(c.verb, c.resource, func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("apiserver is down")
			})

			err := r.Reconcile(context.Background(), key(object))
			if err == nil {
				t.Fatal("want an error so the key is requeued")
			}
			var rej *rejection
			if errors.As(err, &rej) {
				t.Errorf("API error surfaced as a rejection: %v", err)
			}
			if got := stored(t, r, object); got.Status.Phase == v1alpha1.PhaseRejected {
				t.Errorf("phase = Rejected, want it left alone for a retry")
			}
			assertNoWrites(t, r)
		})
	}
}

func TestRejectionError(t *testing.T) {
	err := reject("namespace %q is protected", "kube-system")
	if got, want := err.Error(), `namespace "kube-system" is protected`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// The finalizer must be written with a patch scoped to metadata, never a
// full-object update. A PUT round-trips spec through the typed struct, and a
// field stored empty but tagged omitempty — matchExpressions: [] is the one
// the schema permits — comes back absent. The CRD's immutability rule reads
// that as a spec change and rejects the write, which is not a rejection, so
// no status is ever written: the object wedges with a blank phase, no
// policies, and an error blaming the operator for an edit they never made.
func TestFinalizerIsPatchedNotPut(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	// Stored as an empty array, exactly as the schema's maxItems: 0 permits.
	object.Spec.Peers[0].PodSelector.MatchExpressions = []metav1.LabelSelectorRequirement{}
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var sawPatch bool
	for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		if a.GetSubresource() != "" {
			continue // status writes are a separate concern
		}
		switch a.GetVerb() {
		case "patch":
			sawPatch = true
			body := string(a.(k8stesting.PatchAction).GetPatch())
			if strings.Contains(body, "\"spec\"") {
				t.Errorf("finalizer patch carries spec, which can trip the immutability rule: %s", body)
			}
			if !strings.Contains(body, "finalizers") {
				t.Errorf("finalizer patch does not mention finalizers: %s", body)
			}
		case "update":
			t.Errorf("finalizer written with a full-object update; use a metadata patch")
		}
	}
	if !sawPatch {
		t.Error("no patch issued for the finalizer")
	}

	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseActive {
		t.Errorf("phase = %q (%s), want Active", got.Status.Phase, got.Status.Message)
	}
	if len(got.Finalizers) != 1 {
		t.Errorf("finalizers = %v", got.Finalizers)
	}
}

// The same applies to removing it during cleanup.
func TestFinalizerRemovalIsPatchedNotPut(t *testing.T) {
	object := deleting()
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		if a.GetSubresource() == "" && a.GetVerb() == "update" {
			t.Error("finalizer removed with a full-object update; use a metadata patch")
		}
	}
	if got := stored(t, r, object); len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want empty", got.Finalizers)
	}
}

// An API failure during a pass must still leave the object explaining itself.
// A blank phase is indistinguishable from "the controller is not running", and
// that ambiguity is what made the finalizer-patch RBAC gap invisible until it
// was run in a cluster.
func TestTransientErrorStillReportsStatus(t *testing.T) {
	t.Run("before activation", func(t *testing.T) {
		object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
		r := newReconciler(ns("tenant-a"), ns("tenant-b"))
		r.Dyn = dynClient(t, object)
		r.Kube.(*fake.Clientset).PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("apiserver is down")
		})

		if err := r.Reconcile(context.Background(), key(object)); err == nil {
			t.Fatal("want an error so the key is requeued")
		}
		got := stored(t, r, object)
		if got.Status.Phase != v1alpha1.PhasePending {
			t.Errorf("phase = %q, want Pending: it has not been evaluated yet", got.Status.Phase)
		}
		if !strings.Contains(got.Status.Message, "apiserver is down") {
			t.Errorf("message = %q, want the API server's reason", got.Status.Message)
		}
		if len(got.Finalizers) != 0 {
			t.Errorf("finalizers = %v, want none: nothing was written", got.Finalizers)
		}
	})

	t.Run("after activation", func(t *testing.T) {
		object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
		r := newReconciler(ns("tenant-a"), ns("tenant-b"))
		r.Dyn = dynClient(t, object)
		if err := r.Reconcile(context.Background(), key(object)); err != nil {
			t.Fatalf("activation: %v", err)
		}
		r.Kube.(*fake.Clientset).PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("apiserver is down")
		})

		if err := r.Reconcile(context.Background(), key(object)); err == nil {
			t.Fatal("want an error so the key is requeued")
		}
		got := stored(t, r, object)
		if got.Status.Phase != v1alpha1.PhaseDegraded {
			t.Errorf("phase = %q, want Degraded: the policies are in place", got.Status.Phase)
		}
		if !strings.Contains(got.Status.Message, "apiserver is down") {
			t.Errorf("message = %q, want the API server's reason", got.Status.Message)
		}
	})
}

// Contamination is per-namespace. A foreign ingress policy in one peer's
// namespace is a reason not to write there; it is not a reason to stop
// maintaining the other peer's policy, which is what keeps half the block in
// place. Freezing all repair leaves traffic flowing while status claims the
// policies are retained.
func TestDegradedStillRepairsTheCleanNamespace(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("activation: %v", err)
	}
	names := policy.Names(uid)

	// A platform team drops a default-deny into tenant-a...
	foreign := netpol("tenant-a", "default-deny", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, nil)
	if _, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-a").Create(context.Background(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding the foreign policy: %v", err)
	}
	// ...and, independently, a GitOps prune deletes our policy in tenant-b.
	if err := r.Kube.NetworkingV1().NetworkPolicies("tenant-b").Delete(context.Background(), names[1], metav1.DeleteOptions{}); err != nil {
		t.Fatalf("deleting the owned policy: %v", err)
	}

	if err := r.Reconcile(context.Background(), key(object)); err == nil {
		t.Error("want an error so the object keeps retrying")
	}

	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseDegraded {
		t.Errorf("phase = %q, want Degraded", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, "default-deny") {
		t.Errorf("message = %q, want it to name the foreign policy", got.Status.Message)
	}

	// tenant-b is clean, so its policy must be back: without it, every pod in
	// tenant-a can reach the dashboard.
	if _, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-b").Get(context.Background(), names[1], metav1.GetOptions{}); err != nil {
		t.Errorf("the clean namespace's policy was not repaired: %v", err)
	}
	// tenant-a is contaminated, so nothing new is written there.
	pols, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-a").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pols.Items) != 2 { // the pre-existing owned policy plus the foreign one
		t.Errorf("tenant-a holds %d policies, want the original two untouched", len(pols.Items))
	}
}

// BR-03 still governs first activation: a foreign ingress policy in either
// namespace refuses the whole operation and writes nothing at all.
func TestForeignPolicyBeforeActivationWritesNothing(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	foreign := netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, nil)
	r := newReconciler(ns("tenant-a"), ns("tenant-b"), foreign)
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := stored(t, r, object)
	if got.Status.Phase != v1alpha1.PhaseRejected {
		t.Errorf("phase = %q, want Rejected", got.Status.Phase)
	}
	if len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none", got.Finalizers)
	}
	for _, namespace := range []string{"tenant-a", "tenant-b"} {
		pols, err := r.Kube.NetworkingV1().NetworkPolicies(namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, p := range pols.Items {
			if p.Labels[v1alpha1.OperationLabel] != "" {
				t.Errorf("%s/%s was written despite the foreign policy", namespace, p.Name)
			}
		}
	}
}
