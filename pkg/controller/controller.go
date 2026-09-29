package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// Run starts the informers and a single worker. One worker is enough: the queue
// already serialises a key, and FR-04 assumes a single writer.
func (c *Controller) Run(ctx context.Context) error {
	defer runtime.HandleCrashWithContext(ctx)
	defer c.queue.ShutDown()

	logger := klog.FromContext(ctx)
	logger.Info("Starting controller")
	defer logger.Info("Stopping controller")

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

// Controller watches both custom resources and the policies 001 owns, and
// feeds their keys to the right reconciler through one rate-limited queue and
// one worker. Hardening shares the binary, the queue and the worker with
// isolation (FR-05).
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]
	// reconcile maps a resource name to the reconciler that serves it. One
	// queue, two resources, so a key has to say which it came from.
	reconcile map[string]func(context.Context, string) error
	start     []func(<-chan struct{})
	synced    []cache.InformerSynced
}

// queueKey namespaces a work item by the resource it came from.
func queueKey(resource, object string) string {
	return resource + "|" + object
}

// splitQueueKey undoes queueKey.
func splitQueueKey(key string) (resource, object string, err error) {
	resource, object, found := strings.Cut(key, "|")
	if !found || resource == "" || object == "" {
		return "", "", fmt.Errorf("malformed queue key %q", key)
	}
	return resource, object, nil
}

// New wires the informers.
//
// Both custom resources share one dynamic factory: watching a second resource
// costs a second informer, not a second process, a second queue or a second
// worker. Only policies carrying 001's operation label are watched — hardening
// has no workload informer at all, deliberately, because it does not own the
// fields it writes and a reconcile loop would overwrite a deliberate later
// change and restart pods to do it (FR-05, D-02).
func New(
	kube kubernetes.Interface,
	dyn dynamic.Interface,
	iso *IsolationReconciler,
	hardening *HardeningReconciler,
	resync time.Duration,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		reconcile: map[string]func(context.Context, string) error{
			v1alpha1.IsolationResource.Resource: iso.Reconcile,
			v1alpha1.HardeningResource.Resource: hardening.Reconcile,
		},
	}

	crFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, resync, metav1.NamespaceAll, nil)
	for _, gvr := range []schema.GroupVersionResource{v1alpha1.IsolationResource, v1alpha1.HardeningResource} {
		resource := gvr.Resource
		informer := crFactory.ForResource(gvr).Informer()
		if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { c.enqueue(resource, obj) },
			UpdateFunc: func(_, obj any) { c.enqueue(resource, obj) },
			DeleteFunc: func(obj any) { c.enqueue(resource, obj) },
		}); err != nil {
			return nil, err
		}
		c.synced = append(c.synced, informer.HasSynced)
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
	c.synced = append(c.synced, polInformer.HasSynced)

	c.start = []func(<-chan struct{}){crFactory.Start, polFactory.Start}
	return c, nil
}

func (c *Controller) processNext(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	resource, object, err := splitQueueKey(key)
	if err != nil {
		// Nothing can serve it, so retrying is an infinite loop over a
		// programming error.
		runtime.HandleError(err)
		c.queue.Forget(key)
		return true
	}
	reconcile, ok := c.reconcile[resource]
	if !ok {
		runtime.HandleError(fmt.Errorf("no reconciler for resource %q", resource))
		c.queue.Forget(key)
		return true
	}

	if err := reconcile(ctx, object); err != nil {
		// Retry indefinitely: dropping the key would silently abandon an
		// isolation request, a half-finished cleanup or a half-applied
		// hardening plan.
		runtime.HandleErrorWithLogger(klog.FromContext(ctx), err, "Reconcile failed, retrying", "object", key)
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) enqueue(resource string, obj any) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(queueKey(resource, key))
}

// enqueuePolicy maps a policy event back to the NetworkIsolation that owns it.
// Hardening writes no policies, so a policy event never reaches it.
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
		c.queue.Add(queueKey(v1alpha1.IsolationResource.Resource, key))
	}
}
