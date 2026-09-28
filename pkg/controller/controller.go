package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// Controller watches NetworkIsolation objects and the policies this service
// owns, and feeds their keys to the reconciler through a rate-limited queue.
type Controller struct {
	queue     workqueue.TypedRateLimitingInterface[string]
	reconcile func(context.Context, string) error
	start     []func(<-chan struct{})
	synced    []cache.InformerSynced
}

// New wires the informers. Only policies carrying the operation label are
// watched: a foreign policy appearing later is caught by the resync, and this
// keeps the cache to the objects this service actually owns.
func New(kube kubernetes.Interface, dyn dynamic.Interface, r *Reconciler, resync time.Duration) (*Controller, error) {
	c := &Controller{
		queue:     workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		reconcile: r.Reconcile,
	}

	isoFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, resync, metav1.NamespaceAll, nil)
	isoInformer := isoFactory.ForResource(v1alpha1.Resource).Informer()
	if _, err := isoInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.enqueue(obj) },
		UpdateFunc: func(_, obj any) { c.enqueue(obj) },
		DeleteFunc: func(obj any) { c.enqueue(obj) },
	}); err != nil {
		return nil, err
	}

	polFactory := informers.NewSharedInformerFactoryWithOptions(kube, resync,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.LabelSelector = v1alpha1.OperationLabel
		}))
	polInformer := polFactory.Networking().V1().NetworkPolicies().Informer()
	if _, err := polInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj any) { c.enqueuePolicy(obj) },
		DeleteFunc: func(obj any) { c.enqueuePolicy(obj) },
	}); err != nil {
		return nil, err
	}

	c.start = []func(<-chan struct{}){isoFactory.Start, polFactory.Start}
	c.synced = []cache.InformerSynced{isoInformer.HasSynced, polInformer.HasSynced}
	return c, nil
}

// Run starts the informers and a single worker. One worker is enough: the queue
// already serialises a key, and FR-04 assumes a single writer.
func (c *Controller) Run(ctx context.Context) error {
	defer runtime.HandleCrashWithContext(ctx)
	defer c.queue.ShutDown()

	logger := klog.FromContext(ctx)
	logger.Info("Starting NetworkIsolation controller")
	defer logger.Info("Stopping NetworkIsolation controller")

	for _, start := range c.start {
		start(ctx.Done())
	}
	if !cache.WaitForNamedCacheSyncWithContext(ctx, c.synced...) {
		return errors.New("timed out waiting for caches to sync")
	}

	go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	<-ctx.Done()
	return nil
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.processNext(ctx) {
	}
}

func (c *Controller) processNext(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	if err := c.reconcile(ctx, key); err != nil {
		// Retry indefinitely: dropping the key would silently abandon an
		// isolation request or, worse, a half-finished cleanup.
		runtime.HandleErrorWithLogger(klog.FromContext(ctx), err, "Reconcile failed, retrying", "object", key)
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) enqueue(obj any) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

// enqueuePolicy maps a policy event back to the object that owns it, so a
// deleted or edited policy is repaired on the next pass.
func (c *Controller) enqueuePolicy(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	meta, ok := obj.(metav1.Object)
	if !ok {
		runtime.HandleError(errors.New("policy event carried an object without metadata"))
		return
	}
	if key, ok := ownerKey(meta); ok {
		c.queue.Add(key)
	}
}

// ownerKey reads the "namespace/name" of the owning object from the policy's
// owner annotation.
func ownerKey(meta metav1.Object) (string, bool) {
	v := meta.GetAnnotations()[v1alpha1.OwnerAnnotation]
	namespace, name, found := strings.Cut(v, "/")
	if !found || namespace == "" || name == "" {
		return "", false
	}
	return v, true
}
