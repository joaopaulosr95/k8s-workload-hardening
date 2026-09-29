// Package envtest_test runs the two CRDs this repository ships against a real
// API server and etcd, with no kubelet and no nodes.
//
// It is the only place the structural schemas and the CEL transition rules are
// exercised from Go. The hack/verify-crd-*.sh scripts assert the same things
// through kubectl against a kind cluster; those stay, because they also prove
// the manifests install through `make deploy`. This asserts the schema itself,
// on every pull request, in the fast job.
package envtest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var dyn dynamic.Interface

const group = "hardening.acme.corp"

var (
	isolationGVR = schema.GroupVersionResource{Group: group, Version: "v1alpha1", Resource: "networkisolations"}
	hardeningGVR = schema.GroupVersionResource{Group: group, Version: "v1alpha1", Resource: "workloadhardenings"}
)

// TestMain starts one control plane for the whole package and installs the
// two CRDs from deploy/, so the manifests under test are the ones shipped
// rather than a copy that can drift.
//
// It exits 0 with a message when KUBEBUILDER_ASSETS is unset, so `go test ./...`
// on a machine with no control-plane binaries does not fail. That is also the
// one way this suite can lie — a skip and a pass are indistinguishable in a
// summary line — which is why CI asserts that it actually ran.
func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Println("envtest: KUBEBUILDER_ASSETS is unset; run `make envtest-assets`. Skipping.")
		os.Exit(0)
	}

	env := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				filepath.Join("..", "..", "deploy", "crd-isolation.yaml"),
				filepath.Join("..", "..", "deploy", "crd-hardening.yaml"),
			},
			ErrorIfPathMissing: true,
		},
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	var dynErr error
	if dyn, dynErr = dynamic.NewForConfig(cfg); dynErr != nil {
		fmt.Fprintf(os.Stderr, "dynamic client: %v\n", dynErr)
		_ = env.Stop()
		os.Exit(1)
	}

	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
	}
	os.Exit(code)
}

// apply creates obj, or updates it if it is already stored. Updating rather
// than recreating is what exercises a CEL transition rule at all: oldSelf only
// exists on an update.
func apply(gvr schema.GroupVersionResource, obj map[string]any) error {
	u := &unstructured.Unstructured{Object: obj}
	c := dyn.Resource(gvr).Namespace(u.GetNamespace())
	if stored, err := c.Get(context.Background(), u.GetName(), metav1.GetOptions{}); err == nil {
		u.SetResourceVersion(stored.GetResourceVersion())
		_, err := c.Update(context.Background(), u, metav1.UpdateOptions{})
		return err
	}
	_, err := c.Create(context.Background(), u, metav1.CreateOptions{})
	return err
}

// rejects asserts the API server refuses obj, and refuses it for the stated
// reason. The expected substring is not decoration: without it, an object
// rejected for an unrelated reason — or one that failed to parse — reads as a
// passing test. hack/verify-crd-hardening.sh already carries that rule in a
// comment; this is the same rule in Go.
func rejects(t *testing.T, gvr schema.GroupVersionResource, want string, obj map[string]any) {
	t.Helper()
	err := apply(gvr, obj)
	if err == nil {
		t.Fatalf("accepted; want rejected with %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("rejected for the wrong reason:\n  got:  %v\n  want: %q", err, want)
	}
}

func accepts(t *testing.T, gvr schema.GroupVersionResource, obj map[string]any) {
	t.Helper()
	if err := apply(gvr, obj); err != nil {
		t.Fatalf("rejected; want accepted: %v", err)
	}
}

// object is the boilerplate every case shares.
func object(kind, name string, spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": group + "/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": "default"},
		"spec":       spec,
	}
}
