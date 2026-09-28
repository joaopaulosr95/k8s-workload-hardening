// Command main runs the network-isolation controller.
package main

import (
	"context"
	"flag"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/controller"
)

func main() {
	var (
		kubeconfig = flag.String("kubeconfig", "", "path to a kubeconfig; empty means in-cluster")
		resync     = flag.Duration("resync", 30*time.Second, "how often to re-validate preconditions for every object")
		timeout    = flag.Duration("timeout", 30*time.Second, "deadline for the API calls of one reconcile pass")
		extra      = flag.String("protected-namespaces", "", "comma-separated namespaces to refuse in addition to the built-in ones")
	)
	klog.InitFlags(nil)
	flag.Parse()

	logger := klog.Background()
	ctx, stop := signal.NotifyContext(klog.NewContext(context.Background(), logger), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		logger.Error(err, "Building the Kubernetes client config failed")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Building the Kubernetes client failed")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Building the dynamic client failed")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	protected := protectedNamespaces(*extra)
	logger.Info("Starting", "protectedNamespaces", slices.Sorted(maps.Keys(protected)), "resync", *resync)

	isolation := &controller.Reconciler{
		Kube:      kube,
		Dyn:       dyn,
		Protected: protected,
		Timeout:   *timeout,
		Now:       time.Now,
	}
	// The same clients, the same protected set and the same timeouts. The
	// hardening reconciler keeps its own dry-run cache and nothing else.
	hardening := &controller.HardeningReconciler{
		Kube:      kube,
		Dyn:       dyn,
		Protected: protected,
		Timeout:   *timeout,
		Now:       time.Now,
	}

	c, err := controller.New(kube, dyn, isolation, hardening, *resync)
	if err != nil {
		logger.Error(err, "Building the controller failed")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	if err := c.Run(ctx); err != nil {
		logger.Error(err, "Controller stopped")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
}

// protectedNamespaces is the built-in set, plus this pod's own namespace, plus
// anything the operator named on the flag (BR-05).
func protectedNamespaces(extra string) map[string]bool {
	out := map[string]bool{
		"kube-system":     true,
		"kube-public":     true,
		"kube-node-lease": true,
	}
	if own := os.Getenv("POD_NAMESPACE"); own != "" {
		out[own] = true
	}
	for _, n := range strings.Split(extra, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out
}
