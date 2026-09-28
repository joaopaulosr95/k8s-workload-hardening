package controller

import (
	"context"
	"errors"
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

// A failed reconcile is retried, never dropped: abandoning a key would leave an
// isolation request unserved or a cleanup half-finished.
func TestProcessNextRequeuesOnError(t *testing.T) {
	q := &fakeQueue{items: []string{"isolation-system/gw-dash"}}
	c := &Controller{queue: q, reconcile: func(context.Context, string) error {
		return errors.New("apiserver is down")
	}}

	if !c.processNext(context.Background()) {
		t.Fatal("processNext returned false, want it to keep working")
	}
	if len(q.requeued) != 1 || q.requeued[0] != "isolation-system/gw-dash" {
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
	q := &fakeQueue{items: []string{"isolation-system/gw-dash"}}
	c := &Controller{queue: q, reconcile: func(context.Context, string) error { return nil }}

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
	c := &Controller{queue: &fakeQueue{}, reconcile: func(context.Context, string) error { return nil }}
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
		{"owned policy", owned, []string{"isolation-system/gw-dash"}},
		{"tombstone", cache.DeletedFinalStateUnknown{Key: "tenant-a/netiso-x-0", Obj: owned}, []string{"isolation-system/gw-dash"}},
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

	c, err := New(r.Kube, r.Dyn, r, time.Hour)
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
