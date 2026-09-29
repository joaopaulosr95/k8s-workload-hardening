package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// A policy event must map back to the object that owns it, using the owner
// annotation rather than a lookup table.
func TestOwnerKeyFromPolicy(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		want        string
		ok          bool
	}{
		{"owned", map[string]string{v1alpha1.OwnerAnnotation: "isolation-system/gw-dash"}, "isolation-system/gw-dash", true},
		{"no annotation", nil, "", false},
		{"malformed", map[string]string{v1alpha1.OwnerAnnotation: "not-a-key"}, "", false},
		{"empty value", map[string]string{v1alpha1.OwnerAnnotation: ""}, "", false},
		{"missing namespace", map[string]string{v1alpha1.OwnerAnnotation: "/gw-dash"}, "", false},
		{"missing name", map[string]string{v1alpha1.OwnerAnnotation: "isolation-system/"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ownerKey(&metav1.ObjectMeta{Annotations: c.annotations})
			if ok != c.ok || got != c.want {
				t.Errorf("ownerKey = (%q, %v), want (%q, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

// fakeQueue records what the worker loop does with a key.
type fakeQueue struct {
	workqueue.TypedRateLimitingInterface[string]
	items     []string
	requeued  []string
	forgotten []string
	done      []string
}

func (q *fakeQueue) Get() (string, bool) {
	if len(q.items) == 0 {
		return "", true
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, false
}
func (q *fakeQueue) Add(k string)            { q.items = append(q.items, k) }
func (q *fakeQueue) AddRateLimited(k string) { q.requeued = append(q.requeued, k) }
func (q *fakeQueue) Forget(k string)         { q.forgotten = append(q.forgotten, k) }
func (q *fakeQueue) Done(k string)           { q.done = append(q.done, k) }

// Len is not decoration: processNext publishes queue depth from the consumer,
// and the embedded interface is nil, so without this the promoted method
// dereferences nil and every processNext test segfaults.
func (q *fakeQueue) Len() int { return len(q.items) }

// A failed reconcile is retried, never dropped: abandoning a key would leave an
// isolation request unserved or a cleanup half-finished.
func TestProcessNextRequeuesOnError(t *testing.T) {
	key := queueKey("networkisolations", "isolation-system/gw-dash")
	q := &fakeQueue{items: []string{key}}
	c := &Controller{queue: q, reconcile: map[string]func(context.Context, string) error{
		"networkisolations": func(context.Context, string) error { return errors.New("apiserver is down") },
	}}

	if !c.processNext(context.Background()) {
		t.Fatal("processNext returned false, want it to keep working")
	}
	if len(q.requeued) != 1 || q.requeued[0] != key {
		t.Errorf("requeued = %v, want the key retried", q.requeued)
	}
	if len(q.forgotten) != 0 {
		t.Errorf("forgotten = %v, want the key remembered", q.forgotten)
	}
	if len(q.done) != 1 {
		t.Errorf("done = %v, want the key released exactly once", q.done)
	}
}

func TestProcessNextForgetsOnSuccess(t *testing.T) {
	key := queueKey("networkisolations", "isolation-system/gw-dash")
	q := &fakeQueue{items: []string{key}}
	c := &Controller{queue: q, reconcile: map[string]func(context.Context, string) error{
		"networkisolations": func(context.Context, string) error { return nil },
	}}

	if !c.processNext(context.Background()) {
		t.Fatal("processNext returned false, want it to keep working")
	}
	if len(q.forgotten) != 1 {
		t.Errorf("forgotten = %v, want the backoff history cleared", q.forgotten)
	}
	if len(q.requeued) != 0 {
		t.Errorf("requeued = %v, want none", q.requeued)
	}
}

func TestProcessNextStopsWhenQueueDrains(t *testing.T) {
	c := &Controller{queue: &fakeQueue{}, reconcile: map[string]func(context.Context, string) error{}}
	if c.processNext(context.Background()) {
		t.Error("processNext returned true on a shut-down queue, want false")
	}
}

// A policy event reaches its owner, including when it arrives as a tombstone
// after a missed delete.
func TestEnqueuePolicy(t *testing.T) {
	owned := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "netiso-x-0", Namespace: "tenant-a",
		Annotations: map[string]string{v1alpha1.OwnerAnnotation: "isolation-system/gw-dash"},
	}}
	unowned := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "tenant-a"}}

	cases := []struct {
		name string
		obj  any
		want []string
	}{
		{"owned policy", owned, []string{"networkisolations|isolation-system/gw-dash"}},
		{"tombstone", cache.DeletedFinalStateUnknown{Key: "tenant-a/netiso-x-0", Obj: owned}, []string{"networkisolations|isolation-system/gw-dash"}},
		{"no owner annotation", unowned, nil},
		{"not an object", "a string", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := &fakeQueue{}
			ctrl := &Controller{queue: q}
			ctrl.enqueuePolicy(c.obj)
			if len(q.items) != len(c.want) {
				t.Fatalf("queued %v, want %v", q.items, c.want)
			}
			for i := range c.want {
				if q.items[i] != c.want[i] {
					t.Errorf("queued[%d] = %q, want %q", i, q.items[i], c.want[i])
				}
			}
		})
	}
}

// New must wire both informers and Run must reach cache sync and then exit on
// context cancellation. Driven by the fakes, this proves the wiring holds
// together — a New that silently failed to register a handler, or a Run that
// blocked forever on sync, would show up here and nowhere else in unit tests.
func TestNewAndRun(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	r := newReconciler(ns("tenant-a"), ns("tenant-b"))
	r.Dyn = dynClient(t, object)

	hardener := &HardeningReconciler{
		Kube: r.Kube, Dyn: r.Dyn, Protected: r.Protected, Timeout: r.Timeout, Now: r.Now,
	}
	c, err := New(r.Kube, r.Dyn, r, hardener, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	// The isolation informer should deliver the seeded object, so the
	// reconciler runs and the object reaches Active without any further help.
	deadline := time.After(10 * time.Second)
	for {
		if got := stored(t, r, object); got.Status.Phase == v1alpha1.PhaseActive {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("object never reached Active; phase = %q", stored(t, r, object).Status.Phase)
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Run did not return after the context was cancelled")
	}
}

// One queue serves two custom resources, so a key has to carry the resource it
// came from. A bare "namespace/name" would send a WorkloadHardening to the
// NetworkIsolation reconciler, which would report it NotFound and forget it.
func TestQueueKeyRoundTrip(t *testing.T) {
	key := queueKey(v1alpha1.HardeningResource.Resource, "isolation-system/tenant-hardening")
	if key != "workloadhardenings|isolation-system/tenant-hardening" {
		t.Errorf("queueKey = %q", key)
	}

	resource, object, err := splitQueueKey(key)
	if err != nil {
		t.Fatalf("splitQueueKey: %v", err)
	}
	if resource != "workloadhardenings" || object != "isolation-system/tenant-hardening" {
		t.Errorf("splitQueueKey = (%q, %q)", resource, object)
	}

	for _, bad := range []string{"", "isolation-system/gw-dash", "|isolation-system/gw-dash", "networkisolations|"} {
		if _, _, err := splitQueueKey(bad); err == nil {
			t.Errorf("splitQueueKey(%q) accepted a malformed key", bad)
		}
	}
}

// Each resource reaches its own reconciler, and an unknown one is dropped
// rather than retried forever against a reconciler that cannot serve it.
func TestProcessNextRoutesByResource(t *testing.T) {
	var served []string
	c := &Controller{
		queue: &fakeQueue{items: []string{
			queueKey("networkisolations", "isolation-system/gw-dash"),
			queueKey("workloadhardenings", "isolation-system/tenant-hardening"),
			"unknownresource|isolation-system/whatever",
		}},
		reconcile: map[string]func(context.Context, string) error{
			"networkisolations": func(_ context.Context, key string) error {
				served = append(served, "iso:"+key)
				return nil
			},
			"workloadhardenings": func(_ context.Context, key string) error {
				served = append(served, "hardening:"+key)
				return nil
			},
		},
	}

	for range 3 {
		if !c.processNext(context.Background()) {
			t.Fatal("processNext returned false while the queue still held items")
		}
	}

	want := []string{"iso:isolation-system/gw-dash", "hardening:isolation-system/tenant-hardening"}
	if strings.Join(served, ",") != strings.Join(want, ",") {
		t.Errorf("served = %v, want %v", served, want)
	}
	q := c.queue.(*fakeQueue)
	if len(q.requeued) != 0 {
		t.Errorf("requeued = %v; an unroutable key must be dropped, not retried forever", q.requeued)
	}
	if len(q.done) != 3 {
		t.Errorf("done = %v, want every key released exactly once", q.done)
	}
}
