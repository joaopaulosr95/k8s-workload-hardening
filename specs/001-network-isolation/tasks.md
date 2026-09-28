# On-demand Network Isolation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Go service that, given a `NetworkIsolation` custom resource naming two pod groups, writes two ingress-only NetworkPolicies that block direct A↔B traffic, and removes them when the object is deleted.

**Architecture:** Plain client-go. The custom resource is read and written through the **dynamic client** with `unstructured`, converted to hand-written Go structs via `runtime.DefaultUnstructuredConverter` — no code generation, no generated clientset, no deepcopy functions, no controller-runtime. Pods, namespaces and NetworkPolicies use the typed clientset. Policy generation is a pure function in its own package (NFR-01); the reconciler is a plain struct holding two client interfaces, so both fakes (`kubernetes/fake`, `dynamic/fake`) drive it directly.

**Tech Stack:** Go 1.27.1 · `k8s.io/client-go` v0.37.1 · `k8s.io/api` v0.37.1 · `k8s.io/apimachinery` v0.37.1 · `k8s.io/klog/v2` · kind v0.32.0 · kubectl v1.36.2. Vendored (`vendor/`); no new module dependencies — every package used is already inside `k8s.io/client-go@v0.37.1`.

**Spec:** `specs/001-network-isolation/spec.md` (read it alongside this plan; every task cites the requirement it implements)

---

## Global Constraints

- **Module path:** `github.com/joaopaulosr95/k8s-workload-hardening`. Go 1.27.1.
- **No new module dependencies.** `dynamic`, `dynamic/fake`, `dynamic/dynamicinformer`, `informers`, `kubernetes/fake` all live inside the already-required `k8s.io/client-go` module; they are absent from `vendor/` only because nothing imports them yet. **After adding any new k8s import, run `go mod vendor` and commit the vendor changes in the same commit.**
- **API group/version/kind:** `hardening.k8s.io` / `v1alpha1` / `NetworkIsolation`, plural `networkisolations`, namespaced, status subresource.
- **Finalizer:** exactly `hardening.k8s.io/cleanup`.
- **Ownership label:** exactly `hardening.k8s.io/operation`, value = the object's UID.
- **Owner annotation:** exactly `hardening.k8s.io/owner`, value = `<isolation namespace>/<isolation name>`.
- **Policy names:** `netiso-<uid>-0` and `netiso-<uid>-1`, index-aligned with `spec.peers`. Deterministic, no hashing; the index is stable because `spec` is immutable.
- **Namespace label used in peers:** `kubernetes.io/metadata.name` (set automatically by the API server since v1.21).
- **Protected namespaces (BR-05):** `kube-system`, `kube-public`, `kube-node-lease`, the controller's own namespace, plus anything passed on the `-protected-namespaces` flag.
- **Never** delete, update or adopt a NetworkPolicy that does not carry this operation's UID in `hardening.k8s.io/operation` (BR-03, FR-03).
- **No egress rules, no `ipBlock`, no pod IP enumeration** in anything generated (FR-02).
- **Coverage:** `go test ./pkg/... -cover` must reach ≥90% per package (NFR-05, AGENTS.md). `cmd/` is wiring and is excluded from that number.
- **AGENTS.md role constraint:** do not edit `specs/001-network-isolation/spec.md`. If the implementation needs behaviour the spec does not describe, stop and raise it.
- **Commit style:** conventional commits (`feat:`, `test:`, `fix:`, `docs:`, `chore:`), one per task step where the plan says commit.

## Review Focus

Five conditions the spec implies but which no acceptance criterion names. Each has a test assigned to the task that owns the code.

1. **A participating namespace in `Terminating` state.** Pods list fine, but `NetworkPolicies().Create` is rejected with a forbidden error. Expected: `Degraded` with the API error in the message and an endless retry — not a crash and not a silent `Active`. → Task 5, Step 11.
2. **Deleting an object that never reached activation.** A `Rejected` object carries no finalizer, so `cleanup` must return immediately and must not try to remove a finalizer that isn't there or write a `Deleting` status forever. → Task 6, Step 7.
3. **A stale `resourceVersion` on the finalizer or status write.** Two writes in one pass (finalizer, then status) mean the second must use the version the first returned. Expected: a conflict requeues the key and the next pass succeeds — no crash, no lost finalizer. → Task 5, Step 13.
4. **Prefixed label keys** (`example.com/tier: gold`). The CRD's CEL key rule must accept the `prefix/name` form, and the generated `NotIn` requirement must use the key verbatim. → Task 2, Step 9 and Task 3, Step 5.
5. **An empty-string label value** (`app: ""`), which Kubernetes permits. The generated `NotIn [""]` requirement must still parse through `metav1.LabelSelectorAsSelector` and must still exclude only pods carrying that exact empty value. → Task 2, Step 11.

---

## File Structure

| File                            | Responsibility                                                                                                 |
| ------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `pkg/apis/v1alpha1/types.go`    | The `NetworkIsolation` structs, GVR/GVK, phase and label constants, unstructured conversion helpers. No logic. |
| `pkg/policy/policy.go`          | Pure: spec → the two `NetworkPolicy` objects, and the deterministic names. No clients, no context.             |
| `pkg/controller/validate.go`    | Preconditions BR-03/04/05 and the matched-pod counts. Distinguishes a rejection from an API error.             |
| `pkg/controller/reconcile.go`   | `Reconciler`: one reconcile pass — deletion branch, activation branch, policy apply, status write, cleanup.    |
| `pkg/controller/controller.go`  | Informers, workqueue, `Run`. Replaces the client-go sample boilerplate currently in this file.                 |
| `cmd/main/main.go`              | Flags, clients, signal handling, wiring.                                                                       |
| `deploy/crd.yaml`               | The CRD, structural schema, CEL immutability, status subresource, printer columns.                             |
| `deploy/rbac.yaml`              | Namespace, ServiceAccount, ClusterRole, ClusterRoleBinding.                                                    |
| `deploy/controller.yaml`        | The Deployment.                                                                                                |
| `deploy/samples/workloads.yaml` | tenant-a/tenant-b/tenant-c namespaces, three probe pods, two services.                                         |
| `deploy/samples/isolation.yaml` | An example `NetworkIsolation`.                                                                                 |
| `hack/verify-isolation.sh`      | AC-07, on a live kind cluster.                                                                                 |
| `hack/verify-crd.sh`            | AC-09, on a live cluster.                                                                                      |
| `Makefile`                      | `test`, `cover`, `kind-up`, `image`, `deploy`, `verify`, `verify-crd`.                                         |
| `README.md`                     | Setup, decisions, limitations, versions, time spent.                                                           |

---

### Task 1: API types and scaffold removal

The repository currently holds the client-go sample pod-printer in `pkg/controller/controller.go`. It does not compile as a package (it has a `main` function and an undefined `meta_v1` reference). Clear it, and land the type definitions everything else imports.

**Files:**

- Delete: `pkg/controller/controller.go`, `pkg/controller/controller_test.go`
- Create: `pkg/apis/v1alpha1/types.go`
- Test: `pkg/apis/v1alpha1/types_test.go`

**Interfaces:**

- Consumes: nothing.
- Produces: `v1alpha1.NetworkIsolation`, `v1alpha1.Spec`, `v1alpha1.Group`, `v1alpha1.Status`, `v1alpha1.PeerStatus`, `v1alpha1.Phase` and its five constants, `v1alpha1.Resource` (a `schema.GroupVersionResource`), `v1alpha1.GroupVersionKind`, `v1alpha1.Finalizer`, `v1alpha1.OperationLabel`, `v1alpha1.OwnerAnnotation`, `v1alpha1.FromUnstructured(*unstructured.Unstructured) (*NetworkIsolation, error)`, `v1alpha1.ToUnstructured(*NetworkIsolation) (*unstructured.Unstructured, error)`.

- [ ] **Step 1: Remove the sample-controller scaffold**

```bash
git rm pkg/controller/controller.go pkg/controller/controller_test.go
```

- [ ] **Step 2: Write the failing test**

Create `pkg/apis/v1alpha1/types_test.go`:

```go
package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A dynamic-client object must survive the trip into the typed struct and back
// with the fields the reconciler depends on intact: UID, finalizers, the
// deletion timestamp and both selectors.
func TestUnstructuredRoundTrip(t *testing.T) {
	now := metav1.Now()
	in := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": GroupName + "/" + Version,
		"kind":       Kind,
		"metadata": map[string]any{
			"name":              "gateway-dashboard",
			"namespace":         "isolation-system",
			"uid":               "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8",
			"finalizers":        []any{Finalizer},
			"deletionTimestamp": now.UTC().Format("2006-01-02T15:04:05Z"),
		},
		"spec": map[string]any{
			"peers": []any{
				map[string]any{
					"namespace":   "tenant-a",
					"podSelector": map[string]any{"matchLabels": map[string]any{"app": "gateway"}},
				},
				map[string]any{
					"namespace":   "tenant-b",
					"podSelector": map[string]any{"matchLabels": map[string]any{"app": "dashboard"}},
				},
			},
		},
	}}

	iso, err := FromUnstructured(in)
	if err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if string(iso.UID) != "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Errorf("UID = %q", iso.UID)
	}
	if len(iso.Finalizers) != 1 || iso.Finalizers[0] != Finalizer {
		t.Errorf("Finalizers = %v", iso.Finalizers)
	}
	if iso.DeletionTimestamp == nil {
		t.Error("DeletionTimestamp lost in conversion")
	}
	if len(iso.Spec.Peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(iso.Spec.Peers))
	}
	if iso.Spec.Peers[0].Namespace != "tenant-a" || iso.Spec.Peers[1].PodSelector.MatchLabels["app"] != "dashboard" {
		t.Errorf("Spec = %+v", iso.Spec)
	}

	out, err := ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	if out.GetKind() != Kind || out.GetAPIVersion() != GroupName+"/"+Version {
		t.Errorf("GVK = %s %s", out.GetAPIVersion(), out.GetKind())
	}
	if out.GetUID() != iso.UID {
		t.Errorf("UID = %q", out.GetUID())
	}
}

// Zero counts are part of the report, not an absence (FR-05), so they must not
// be dropped by omitempty on the way out.
func TestZeroCountsAreSerialised(t *testing.T) {
	iso := &NetworkIsolation{Status: Status{
		Phase: PhaseActive,
		Peers: []PeerStatus{{Policy: "netiso-x-0", Matched: 0}, {Policy: "netiso-x-1", Matched: 0}},
	}}
	out, err := ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	status, ok := out.Object["status"].(map[string]any)
	if !ok {
		t.Fatal("status missing")
	}
	peers, ok := status["peers"].([]any)
	if !ok || len(peers) != 2 {
		t.Fatalf("status.peers = %v", status["peers"])
	}
	for i, raw := range peers {
		peer := raw.(map[string]any)
		if _, ok := peer["matched"]; !ok {
			t.Errorf("peer %d: matched dropped when zero", i)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./pkg/apis/... -v`
Expected: FAIL — `no required module provides package .../pkg/apis/v1alpha1` / build failure, no such package.

- [ ] **Step 4: Write the types**

Create `pkg/apis/v1alpha1/types.go`:

```go
// Package v1alpha1 holds the NetworkIsolation API types. They are plain structs
// converted to and from unstructured objects, so the project needs no generated
// clientset and no deepcopy functions.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	GroupName = "hardening.k8s.io"
	Version   = "v1alpha1"
	Kind      = "NetworkIsolation"

	// Finalizer is persisted before the first policy write, so cleanup is
	// guaranteed a chance to run (FR-03).
	Finalizer = "hardening.k8s.io/cleanup"
	// OperationLabel carries the owning object's UID on every generated policy.
	// Only policies bearing it are ever updated or deleted.
	OperationLabel = "hardening.k8s.io/operation"
	// OwnerAnnotation records "<namespace>/<name>" of the owning object, so a
	// policy event can be mapped back to the object without a lookup table.
	OwnerAnnotation = "hardening.k8s.io/owner"
)

// Resource is the GVR the dynamic client uses for NetworkIsolation objects.
var Resource = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "networkisolations"}

// GroupVersionKind stamps unstructured objects on the way out.
var GroupVersionKind = schema.GroupVersionKind{Group: GroupName, Version: Version, Kind: Kind}

// Phase is the coarse state reported in status (FR-05).
type Phase string

const (
	PhasePending  Phase = "Pending"
	PhaseRejected Phase = "Rejected"
	PhaseActive   Phase = "Active"
	PhaseDegraded Phase = "Degraded"
	PhaseDeleting Phase = "Deleting"
)

// Group is one of the two pod sets being isolated. Membership is whatever the
// selector matches right now; the controller never reads workload objects.
type Group struct {
	Namespace   string               `json:"namespace"`
	PodSelector metav1.LabelSelector `json:"podSelector"`
}

// Spec is immutable once created, enforced by a CEL rule in the CRD (FR-01).
// Peers always holds exactly two entries: the CRD pins the length, because one
// pair needs no policy compiler and several pairs are separate objects (D-01).
// The two are symmetric — the block is mutual, and their order carries no
// meaning beyond indexing the generated policies.
type Spec struct {
	Peers []Group `json:"peers"`
}

// PeerStatus is what the controller observed about one peer. Matched carries no
// omitempty: zero is an observation and is reported explicitly (FR-05).
type PeerStatus struct {
	Policy  string `json:"policy"`
	Matched int    `json:"matched"`
}

// Status reports what the controller observed. Peers is index-aligned with
// Spec.Peers.
type Status struct {
	Phase             Phase        `json:"phase,omitempty"`
	Message           string       `json:"message,omitempty"`
	Peers             []PeerStatus `json:"peers,omitempty"`
	LastReconcileTime string       `json:"lastReconcileTime,omitempty"`
}

type NetworkIsolation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Spec   `json:"spec"`
	Status            Status `json:"status,omitempty"`
}

// FromUnstructured converts an object read through the dynamic client.
func FromUnstructured(u *unstructured.Unstructured) (*NetworkIsolation, error) {
	iso := &NetworkIsolation{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, iso); err != nil {
		return nil, err
	}
	return iso, nil
}

// ToUnstructured converts back for a write through the dynamic client.
func ToUnstructured(iso *NetworkIsolation) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(iso)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(GroupVersionKind)
	return u, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/... -v`
Expected: PASS, both tests.

- [ ] **Step 6: Commit**

```bash
go mod vendor
git add -A pkg vendor go.mod go.sum
git commit -m "feat(api): add NetworkIsolation types and unstructured conversion

Drops the client-go sample-controller scaffold in the same pass."
```

---

### Task 2: Policy generation (AC-01, AC-02)

The pure core of the feature, and the part the spec flags as easiest to get wrong. Build it against a truth table rather than against expected YAML, so an inverted negation fails the test.

**Files:**

- Create: `pkg/policy/policy.go`
- Test: `pkg/policy/policy_test.go`

**Interfaces:**

- Consumes: `v1alpha1.NetworkIsolation`, `v1alpha1.Group`, `v1alpha1.OperationLabel`, `v1alpha1.OwnerAnnotation`.
- Produces: `policy.Name(uid string, i int) string`, `policy.Names(uid string) []string`, `policy.Build(iso *v1alpha1.NetworkIsolation) []*networkingv1.NetworkPolicy` — a slice index-aligned with `spec.peers`: element `i` protects `peers[i]`, lives in its namespace, and excludes the other.

- [ ] **Step 1: Write the failing shape test (AC-01)**

Create `pkg/policy/policy_test.go`:

```go
package policy

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

const testUID = "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8"

func isolation(aNS string, aLabels map[string]string, bNS string, bLabels map[string]string) *v1alpha1.NetworkIsolation {
	return &v1alpha1.NetworkIsolation{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-dash", Namespace: "isolation-system", UID: testUID},
		Spec: v1alpha1.Spec{Peers: []v1alpha1.Group{
			{Namespace: aNS, PodSelector: metav1.LabelSelector{MatchLabels: aLabels}},
			{Namespace: bNS, PodSelector: metav1.LabelSelector{MatchLabels: bLabels}},
		}},
	}
}

// AC-01: exactly two ingress-only policies, each in its own group's namespace,
// each selecting its own group, with no egress and no ipBlock anywhere.
func TestBuildShape(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"app": "dashboard", "tier": "web"})

	got := Build(iso)
	if len(got) != 2 {
		t.Fatalf("got %d policies, want 2", len(got))
	}

	names := Names(testUID)
	cases := []struct {
		p        *networkingv1.NetworkPolicy
		name, ns string
		selector map[string]string
		peers    int
	}{
		{got[0], names[0], "tenant-a", map[string]string{"app": "gateway"}, 3},                  // 1 + len(peers[1] labels)
		{got[1], names[1], "tenant-b", map[string]string{"app": "dashboard", "tier": "web"}, 2}, // 1 + len(peers[0] labels)
	}
	for _, c := range cases {
		if c.p.Name != c.name || c.p.Namespace != c.ns {
			t.Errorf("policy = %s/%s, want %s/%s", c.p.Namespace, c.p.Name, c.ns, c.name)
		}
		if c.p.Labels[v1alpha1.OperationLabel] != testUID {
			t.Errorf("%s: operation label = %q", c.p.Name, c.p.Labels[v1alpha1.OperationLabel])
		}
		if c.p.Annotations[v1alpha1.OwnerAnnotation] != "isolation-system/gw-dash" {
			t.Errorf("%s: owner annotation = %q", c.p.Name, c.p.Annotations[v1alpha1.OwnerAnnotation])
		}
		if len(c.p.Spec.PolicyTypes) != 1 || c.p.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
			t.Errorf("%s: policyTypes = %v, want [Ingress]", c.p.Name, c.p.Spec.PolicyTypes)
		}
		if c.p.Spec.Egress != nil {
			t.Errorf("%s: egress rules present", c.p.Name)
		}
		for k, v := range c.selector {
			if c.p.Spec.PodSelector.MatchLabels[k] != v {
				t.Errorf("%s: podSelector = %v, want %v", c.p.Name, c.p.Spec.PodSelector.MatchLabels, c.selector)
			}
		}
		if len(c.p.Spec.Ingress) != 1 {
			t.Fatalf("%s: got %d ingress rules, want 1", c.p.Name, len(c.p.Spec.Ingress))
		}
		if c.p.Spec.Ingress[0].Ports != nil {
			t.Errorf("%s: port restrictions present, want none (BR-01)", c.p.Name)
		}
		peers := c.p.Spec.Ingress[0].From
		if len(peers) != c.peers {
			t.Errorf("%s: got %d peers, want %d", c.p.Name, len(peers), c.peers)
		}
		for _, peer := range peers {
			if peer.IPBlock != nil {
				t.Errorf("%s: ipBlock present (FR-02 forbids it)", c.p.Name)
			}
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/policy/ -run TestBuildShape -v`
Expected: FAIL — build error, `undefined: Build`, `undefined: Names`.

- [ ] **Step 3: Write the implementation**

Create `pkg/policy/policy.go`:

```go
// Package policy turns a NetworkIsolation spec into the two NetworkPolicies
// that implement it. It is pure — no clients, no context, no clock — so the
// hard part of this feature is testable on its own (NFR-01).
package policy

import (
	"maps"
	"slices"
	"strconv"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// namespaceNameLabel is applied to every namespace by the API server since
// v1.21, which is what lets a peer name a namespace without the operator
// having to label it.
const namespaceNameLabel = "kubernetes.io/metadata.name"

// Name returns the deterministic name of the policy protecting peer i (FR-03).
// A UID is 36 characters of lowercase hex and hyphens, so the result is always
// a valid DNS-1123 subdomain and needs no hashing or truncation. The index is
// stable because the spec is immutable.
func Name(uid string, i int) string {
	return "netiso-" + uid + "-" + strconv.Itoa(i)
}

// Names returns the policy names for an operation, index-aligned with
// spec.peers.
func Names(uid string) []string {
	return []string{Name(uid, 0), Name(uid, 1)}
}

// Build returns the two policies for iso, index-aligned with spec.peers:
// element i protects peers[i], lives in its namespace, and excludes the other.
func Build(iso *v1alpha1.NetworkIsolation) []*networkingv1.NetworkPolicy {
	uid := string(iso.UID)
	owner := iso.Namespace + "/" + iso.Name
	out := make([]*networkingv1.NetworkPolicy, len(iso.Spec.Peers))
	for i, self := range iso.Spec.Peers {
		other := iso.Spec.Peers[len(iso.Spec.Peers)-1-i]
		out[i] = build(Name(uid, i), uid, owner, self, other)
	}
	return out
}

// build produces the ingress-only policy that protects self by allowing every
// pod source except other. NetworkPolicy has no deny rule, so a prohibition is
// written as an allowance of the complement (FR-02).
func build(name, uid, owner string, self, other v1alpha1.Group) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   self.Namespace,
			Labels:      map[string]string{v1alpha1.OperationLabel: uid},
			Annotations: map[string]string{v1alpha1.OwnerAnnotation: owner},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: self.PodSelector,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: complement(other)}},
		},
	}
}

// complement lists the peers whose union is exactly "every pod that is not g".
func complement(g v1alpha1.Group) []networkingv1.NetworkPolicyPeer {
	peers := []networkingv1.NetworkPolicyPeer{{
		// Branch 1: every pod in every namespace other than g's. A peer with
		// only a namespaceSelector means all pods in the matching namespaces.
		NamespaceSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      namespaceNameLabel,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{g.Namespace},
			}},
		},
	}}

	// Branch 2, one peer per requirement: inside g's namespace, the pods that
	// fail that requirement. NotIn also matches a pod that lacks the key
	// entirely, which is what makes the union complete. The namespaceSelector
	// is not optional here: the policy lives in the *other* group's namespace,
	// so a bare podSelector would select the wrong namespace.
	//
	// Keys are sorted so repeated reconciles of an unchanged object produce a
	// byte-identical spec and therefore no write (AC-06).
	for _, k := range slices.Sorted(maps.Keys(g.PodSelector.MatchLabels)) {
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{namespaceNameLabel: g.Namespace},
			},
			PodSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      k,
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{g.PodSelector.MatchLabels[k]},
				}},
			},
		})
	}
	return peers
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./pkg/policy/ -run TestBuildShape -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/policy
git commit -m "feat(policy): generate the two complement ingress policies"
```

- [ ] **Step 6: Write the failing truth-table test (AC-02)**

This is the test the spec calls out as most easily got wrong. It evaluates the generated peers with the same selector machinery the API server uses, so an inverted or incomplete negation fails here.

Append to `pkg/policy/policy_test.go`:

```go
// allowed reports whether a pod in namespace ns carrying ls is matched by any
// of the peers — that is, whether the generated policy would admit it.
func allowed(t *testing.T, peers []networkingv1.NetworkPolicyPeer, ns string, ls map[string]string) bool {
	t.Helper()
	for _, peer := range peers {
		nsSel, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector)
		if err != nil {
			t.Fatalf("namespaceSelector does not parse: %v", err)
		}
		if !nsSel.Matches(labels.Set{"kubernetes.io/metadata.name": ns}) {
			continue
		}
		if peer.PodSelector == nil {
			return true // whole namespace
		}
		podSel, err := metav1.LabelSelectorAsSelector(peer.PodSelector)
		if err != nil {
			t.Fatalf("podSelector does not parse: %v", err)
		}
		if podSel.Matches(labels.Set(ls)) {
			return true
		}
	}
	return false
}

// AC-02: the complement of B admits everything that is not B, and admits
// nothing that is. The missing-key row is the one that catches a negation
// written as "different value" instead of "fails the requirement".
func TestComplementTruthTable(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"app": "dashboard", "tier": "web"})
	peers := Build(iso)[0].Spec.Ingress[0].From // protects A, excludes B

	cases := []struct {
		name    string
		ns      string
		labels  map[string]string
		allowed bool
	}{
		{"B itself is excluded", "tenant-b", map[string]string{"app": "dashboard", "tier": "web"}, false},
		{"B plus extra labels is still B", "tenant-b", map[string]string{"app": "dashboard", "tier": "web", "x": "y"}, false},
		{"different value for one key", "tenant-b", map[string]string{"app": "worker", "tier": "web"}, true},
		{"missing one key entirely", "tenant-b", map[string]string{"app": "dashboard"}, true},
		{"missing both keys", "tenant-b", nil, true},
		{"unrelated pod in B's namespace", "tenant-b", map[string]string{"app": "cache"}, true},
		{"same labels but another namespace", "tenant-c", map[string]string{"app": "dashboard", "tier": "web"}, true},
		{"a pod in A's own namespace", "tenant-a", map[string]string{"app": "gateway"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := allowed(t, peers, c.ns, c.labels); got != c.allowed {
				t.Errorf("allowed=%v, want %v", got, c.allowed)
			}
		})
	}
}

// AC-02, shared-namespace half: both groups in one namespace must still
// complement each other, and neither policy may exclude the other's neighbours.
func TestComplementSharedNamespace(t *testing.T) {
	iso := isolation("tenant-x", map[string]string{"role": "gateway"},
		"tenant-x", map[string]string{"role": "dashboard"})
	built := Build(iso)
	protectsA, protectsB := built[0].Spec.Ingress[0].From, built[1].Spec.Ingress[0].From

	if allowed(t, protectsA, "tenant-x", map[string]string{"role": "dashboard"}) {
		t.Error("policy protecting A admits B")
	}
	if allowed(t, protectsB, "tenant-x", map[string]string{"role": "gateway"}) {
		t.Error("policy protecting B admits A")
	}
	if !allowed(t, protectsA, "tenant-x", map[string]string{"role": "gateway"}) {
		t.Error("policy protecting A blocks A's own pods")
	}
	if !allowed(t, protectsA, "tenant-x", map[string]string{"role": "cache"}) {
		t.Error("policy protecting A blocks an unrelated pod in the shared namespace")
	}
	if !allowed(t, protectsA, "tenant-y", map[string]string{"role": "dashboard"}) {
		t.Error("policy protecting A blocks a same-labelled pod in another namespace")
	}
}
```

- [ ] **Step 7: Run it to verify it passes**

Run: `go test ./pkg/policy/ -v`
Expected: PASS — the implementation from Step 3 already satisfies these. If any row fails, the negation is wrong; fix `complement`, not the table.

- [ ] **Step 8: Commit**

```bash
git add pkg/policy/policy_test.go
git commit -m "test(policy): complement truth table for AC-02"
```

- [ ] **Step 9: Add the prefixed-key test (Review Focus 4)**

Append to `pkg/policy/policy_test.go`:

```go
// Label keys may carry a DNS-subdomain prefix. The generated requirement must
// use the key verbatim rather than mangling the slash.
func TestPrefixedLabelKey(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"example.com/tier": "gold"})
	peers := Build(iso)[0].Spec.Ingress[0].From

	if allowed(t, peers, "tenant-b", map[string]string{"example.com/tier": "gold"}) {
		t.Error("prefixed-key group not excluded")
	}
	if !allowed(t, peers, "tenant-b", map[string]string{"example.com/tier": "silver"}) {
		t.Error("different value for a prefixed key should be allowed")
	}
	if !allowed(t, peers, "tenant-b", map[string]string{"tier": "gold"}) {
		t.Error("unprefixed key is a different key and should be allowed")
	}
}
```

- [ ] **Step 10: Run it**

Run: `go test ./pkg/policy/ -run TestPrefixedLabelKey -v`
Expected: PASS.

- [ ] **Step 11: Add the empty-value test (Review Focus 5)**

Append to `pkg/policy/policy_test.go`:

```go
// An empty string is a legal label value. The NotIn requirement built from it
// must parse and must exclude only pods carrying that exact empty value.
func TestEmptyLabelValue(t *testing.T) {
	iso := isolation("tenant-a", map[string]string{"app": "gateway"},
		"tenant-b", map[string]string{"app": ""})
	peers := Build(iso)[0].Spec.Ingress[0].From

	if allowed(t, peers, "tenant-b", map[string]string{"app": ""}) {
		t.Error("pod carrying the empty value was not excluded")
	}
	if !allowed(t, peers, "tenant-b", map[string]string{"app": "dashboard"}) {
		t.Error("pod with a non-empty value should be allowed")
	}
	if !allowed(t, peers, "tenant-b", nil) {
		t.Error("pod lacking the key should be allowed")
	}
}
```

- [ ] **Step 12: Run the whole package and check coverage**

Run: `go test ./pkg/policy/ -cover -v`
Expected: PASS, coverage ≥90%.

- [ ] **Step 13: Commit**

```bash
git add pkg/policy/policy_test.go
git commit -m "test(policy): prefixed label keys and empty label values"
```

---

### Task 3: CRD manifest and API-server validation script (AC-09)

The schema is the first line of defence: it rejects shapes the controller then never has to handle.

**Files:**

- Create: `deploy/crd.yaml`, `hack/verify-crd.sh`
- Modify: `Makefile`

**Interfaces:**

- Consumes: the field names from `pkg/apis/v1alpha1/types.go` (Task 1). The schema and the Go structs must agree exactly.
- Produces: an installable CRD; `make verify-crd`.

- [ ] **Step 1: Write the CRD**

Create `deploy/crd.yaml`:

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: networkisolations.hardening.acme.corp
spec:
  group: hardening.acme.corp
  scope: Namespaced
  names:
    plural: networkisolations
    singular: networkisolation
    kind: NetworkIsolation
    shortNames: [netiso]
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      additionalPrinterColumns:
        - name: Phase
          type: string
          jsonPath: .status.phase
        - name: Peer-0
          type: integer
          jsonPath: .status.peers[0].matched
        - name: Peer-1
          type: integer
          jsonPath: .status.peers[1].matched
        - name: Message
          type: string
          jsonPath: .status.message
        - name: Age
          type: date
          jsonPath: .metadata.creationTimestamp
      schema:
        openAPIV3Schema:
          type: object
          description: >-
            Blocks direct network traffic between two pod groups. Creating the
            object requests isolation; deleting it restores the previous
            connectivity.
          required: [spec]
          properties:
            spec:
              type: object
              required: [peers]
              x-kubernetes-validations:
                - rule: self == oldSelf
                  message: >-
                    spec is immutable; delete this object, wait for cleanup,
                    and create a new one to retarget
              properties:
                peers:
                  type: array
                  description: >-
                    Exactly two pod groups. They are symmetric: traffic is
                    blocked between them in both directions, and their order
                    carries no meaning beyond indexing the generated policies.
                  minItems: 2
                  maxItems: 2
                  items:
                    type: object
                    required: [namespace, podSelector]
                    properties:
                      namespace:
                        type: string
                        maxLength: 63
                        pattern: "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
                      podSelector:
                        type: object
                        required: [matchLabels]
                        properties:
                          matchLabels:
                            type: object
                            description: 1-8 equality requirements. All must match.
                            minProperties: 1
                            maxProperties: 8
                            additionalProperties:
                              type: string
                              maxLength: 63
                              pattern: "^(|[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)$"
                            x-kubernetes-validations:
                              - rule: >-
                                  self.all(k, k.matches('^([a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$'))
                                message: every key must be a valid Kubernetes label key
                          matchExpressions:
                            type: array
                            description: >-
                              Not supported. The controller generates the
                              negations itself; operators supply equality maps
                              only.
                            maxItems: 0
                            items:
                              type: object
                              properties:
                                key: { type: string }
                                operator: { type: string }
                                values:
                                  type: array
                                  items: { type: string }
            status:
              type: object
              properties:
                phase:
                  type: string
                  enum: [Pending, Rejected, Active, Degraded, Deleting]
                message:
                  type: string
                peers:
                  type: array
                  description: Index-aligned with spec.peers.
                  items:
                    type: object
                    properties:
                      policy: { type: string }
                      matched: { type: integer }
                lastReconcileTime:
                  type: string
                  format: date-time
```

- [ ] **Step 2: Bring a cluster up and install the CRD**

```bash
kind create cluster --name hardening --config hack/kind/cluster.yaml
kubectl apply -f deploy/crd.yaml
kubectl get crd networkisolations.hardening.acme.corp
```

Expected: `customresourcedefinition.apiextensions.k8s.io/networkisolations.hardening.k8s.io created`, then the CRD listed. A schema error appears here, not later.

- [ ] **Step 3: Write the validation script**

Create `hack/verify-crd.sh`:

```bash
#!/usr/bin/env bash
# AC-09: the CRD installs and the API server rejects the shapes the controller
# should never have to handle.
set -euo pipefail

ns=crd-validation-test
fail=0

cleanup() { kubectl delete namespace "$ns" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

# expect_reject <description> <<<manifest
expect_reject() {
  local what=$1
  if kubectl apply -f - >/dev/null 2>&1; then
    echo "FAIL  accepted: $what"
    fail=1
  else
    echo "ok    rejected: $what"
  fi
}

kubectl apply -f deploy/crd.yaml >/dev/null
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

expect_reject "object missing group b" <<EOF
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata: {name: missing-b, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
EOF

expect_reject "matchExpressions supplied" <<EOF
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata: {name: with-expressions, namespace: $ns}
spec:
  a:
    namespace: tenant-a
    podSelector:
      matchLabels: {app: gateway}
      matchExpressions: [{key: app, operator: In, values: [gateway]}]
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

expect_reject "empty matchLabels" <<EOF
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata: {name: empty-labels, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

expect_reject "invalid namespace name" <<EOF
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata: {name: bad-namespace, namespace: $ns}
spec:
  a: {namespace: Tenant_A, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: dashboard}}}
EOF

# A prefixed label key is legal and must be accepted (Review Focus 4).
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {example.com/tier: gold}}}
EOF
echo "ok    accepted: valid object with a prefixed label key"

expect_reject "edit to an immutable spec" <<EOF
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {app: retargeted}}}
EOF

# Re-applying the identical spec must still be allowed: self == oldSelf holds.
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata: {name: valid, namespace: $ns}
spec:
  a: {namespace: tenant-a, podSelector: {matchLabels: {app: gateway}}}
  b: {namespace: tenant-b, podSelector: {matchLabels: {example.com/tier: gold}}}
EOF
echo "ok    accepted: re-apply of an unchanged spec"

if [ "$fail" -ne 0 ]; then
  echo "AC-09 FAILED"
  exit 1
fi
echo "AC-09 PASSED"
```

- [ ] **Step 4: Run it**

```bash
chmod +x hack/verify-crd.sh
./hack/verify-crd.sh
```

Expected: every line `ok`, final `AC-09 PASSED`. If "matchExpressions supplied" is _accepted_, the field is being pruned instead of rejected — check that `matchExpressions` is declared with `maxItems: 0` rather than left out of the schema.

- [ ] **Step 5: Confirm the CEL key rule accepts a prefixed key**

The valid-object step above already covers Review Focus 4 at the API-server level. Confirm it appeared as `ok    accepted: valid object with a prefixed label key` in the output. If it was rejected, the CEL regex is escaping `.` wrongly — CEL uses RE2, so `[.]` is the safe spelling.

- [ ] **Step 6: Add Makefile targets**

Append to `Makefile`:

```make
.PHONY: test cover verify-crd

test:
	go test ./pkg/... -race

cover:
	go test ./pkg/... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

verify-crd:
	./hack/verify-crd.sh
```

- [ ] **Step 7: Commit**

```bash
git add deploy/crd.yaml hack/verify-crd.sh Makefile
git commit -m "feat(crd): NetworkIsolation schema with immutable spec

Adds hack/verify-crd.sh covering AC-09 against a live API server."
```

---

### Task 4: Preconditions (AC-03, AC-05)

Everything that can refuse an operation before a single byte is written. The foreign-ingress-policy check is the second case the spec flags as easily got wrong.

**Files:**

- Create: `pkg/controller/validate.go`
- Test: `pkg/controller/validate_test.go`

**Interfaces:**

- Consumes: `v1alpha1.*` (Task 1).
- Produces:
  - `type Reconciler struct { Kube kubernetes.Interface; Dyn dynamic.Interface; Protected map[string]bool; Timeout time.Duration; Now func() time.Time }`
  - `type rejection struct{ reason string }` with `Error() string` — a precondition failure, distinct from an API error
  - `func reject(format string, args ...any) error`
  - `type counts []int` — matched pod count per peer, index-aligned with `spec.peers`
  - `func (r *Reconciler) validate(ctx context.Context, iso *v1alpha1.NetworkIsolation) (counts, error)`
  - `func disjoint(a, b map[string]string) bool`
  - `func affectsIngress(p *networkingv1.NetworkPolicy) bool`

  `Reconciler` is declared here and used by Tasks 5, 6 and 7; do not redeclare it.

- [ ] **Step 1: Write the failing rejection tests (AC-05)**

Create `pkg/controller/validate_test.go`:

```go
package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

const uid = "6f1b2c33-4d5e-6f70-8192-a3b4c5d6e7f8"

func ns(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"kubernetes.io/metadata.name": name},
	}}
}

func pod(namespace, name string, labels map[string]string, hostNetwork bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       corev1.PodSpec{HostNetwork: hostNetwork},
	}
}

func netpol(namespace, name string, policyTypes []networkingv1.PolicyType, labels map[string]string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: policyTypes},
	}
}

func iso(aNS string, aLabels map[string]string, bNS string, bLabels map[string]string) *v1alpha1.NetworkIsolation {
	return &v1alpha1.NetworkIsolation{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-dash", Namespace: "isolation-system", UID: uid},
		Spec: v1alpha1.Spec{Peers: []v1alpha1.Group{
			{Namespace: aNS, PodSelector: metav1.LabelSelector{MatchLabels: aLabels}},
			{Namespace: bNS, PodSelector: metav1.LabelSelector{MatchLabels: bLabels}},
		}},
	}
}

// newReconciler builds a Reconciler over a fake typed clientset seeded with
// objects. The dynamic client is filled in by the reconcile tests; validation
// never touches it.
func newReconciler(objects ...runtime.Object) *Reconciler {
	return &Reconciler{
		Kube: fake.NewSimpleClientset(objects...),
		Protected: map[string]bool{
			"kube-system": true, "kube-public": true, "kube-node-lease": true,
			"isolation-system": true,
		},
		Timeout: 5 * time.Second,
		Now:     func() time.Time { return time.Unix(1700000000, 0) },
	}
}

// AC-05: each precondition failure is a distinct, named reason, and the fake
// records no writes of any kind.
func TestValidateRejections(t *testing.T) {
	cases := []struct {
		name    string
		objects []runtime.Object
		iso     *v1alpha1.NetworkIsolation
		wants   string
	}{
		{
			name:    "protected namespace",
			objects: []runtime.Object{ns("kube-system"), ns("tenant-b")},
			iso:     iso("kube-system", map[string]string{"k8s-app": "kube-dns"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants:   "protected",
		},
		{
			name:    "controller's own namespace",
			objects: []runtime.Object{ns("isolation-system"), ns("tenant-b")},
			iso:     iso("isolation-system", map[string]string{"app": "x"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants:   "protected",
		},
		{
			name:    "missing namespace",
			objects: []runtime.Object{ns("tenant-a")},
			iso:     iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants:   "does not exist",
		},
		{
			name:    "overlapping selectors in a shared namespace",
			objects: []runtime.Object{ns("tenant-x")},
			iso:     iso("tenant-x", map[string]string{"app": "gateway"}, "tenant-x", map[string]string{"tier": "web"}),
			wants:   "disjoint",
		},
		{
			name:    "identical selectors in a shared namespace",
			objects: []runtime.Object{ns("tenant-x")},
			iso:     iso("tenant-x", map[string]string{"app": "gateway"}, "tenant-x", map[string]string{"app": "gateway"}),
			wants:   "disjoint",
		},
		{
			name: "matching hostNetwork pod",
			objects: []runtime.Object{
				ns("tenant-a"), ns("tenant-b"),
				pod("tenant-a", "gw-0", map[string]string{"app": "gateway"}, true),
			},
			iso:   iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}),
			wants: "hostNetwork",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReconciler(c.objects...)
			_, err := r.validate(context.Background(), c.iso)

			var rej *rejection
			if !errorsAs(err, &rej) {
				t.Fatalf("err = %v, want a *rejection", err)
			}
			if !strings.Contains(rej.reason, c.wants) {
				t.Errorf("reason = %q, want it to mention %q", rej.reason, c.wants)
			}
			assertNoWrites(t, r)
		})
	}
}

// AC-03: an ingress-affecting foreign policy in either namespace refuses the
// operation; an explicitly egress-only one does not.
func TestValidateForeignPolicies(t *testing.T) {
	cases := []struct {
		name     string
		policy   *networkingv1.NetworkPolicy
		rejected bool
	}{
		{"omitted policyTypes defaults to Ingress", netpol("tenant-b", "legacy", nil, nil), true},
		{"empty policyTypes defaults to Ingress", netpol("tenant-b", "legacy", []networkingv1.PolicyType{}, nil), true},
		{"explicit Ingress", netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, nil), true},
		{"Ingress and Egress", netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeEgress, networkingv1.PolicyTypeIngress}, nil), true},
		{"explicitly egress-only", netpol("tenant-b", "legacy", []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, nil), false},
		{
			"our own policy from a previous pass",
			netpol("tenant-b", "netiso-"+uid+"-b", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				map[string]string{v1alpha1.OperationLabel: uid}),
			false,
		},
		{
			"another operation's policy is foreign",
			netpol("tenant-b", "netiso-other-b", []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				map[string]string{v1alpha1.OperationLabel: "some-other-uid"}),
			true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReconciler(ns("tenant-a"), ns("tenant-b"), c.policy)
			_, err := r.validate(context.Background(),
				iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}))

			var rej *rejection
			gotRejection := errorsAs(err, &rej)
			if gotRejection != c.rejected {
				t.Fatalf("rejected = %v (err %v), want %v", gotRejection, err, c.rejected)
			}
			assertNoWrites(t, r)

			// The foreign policy is never touched, whatever the verdict.
			got, getErr := r.Kube.NetworkingV1().NetworkPolicies(c.policy.Namespace).Get(context.Background(), c.policy.Name, metav1.GetOptions{})
			if getErr != nil {
				t.Fatalf("foreign policy disappeared: %v", getErr)
			}
			if len(got.Spec.PolicyTypes) != len(c.policy.Spec.PolicyTypes) {
				t.Error("foreign policy was modified")
			}
		})
	}
}

// Zero-match selectors are valid and report zero (BR-04, AC-06).
func TestValidateCounts(t *testing.T) {
	r := newReconciler(
		ns("tenant-a"), ns("tenant-b"),
		pod("tenant-a", "gw-0", map[string]string{"app": "gateway"}, false),
		pod("tenant-a", "gw-1", map[string]string{"app": "gateway"}, false),
		pod("tenant-a", "other", map[string]string{"app": "cache"}, false),
	)
	c, err := r.validate(context.Background(),
		iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"}))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(c) != 2 {
		t.Fatalf("got %d counts, want 2", len(c))
	}
	if c[0] != 2 {
		t.Errorf("counts[0] = %d, want 2", c[0])
	}
	if c[1] != 0 {
		t.Errorf("counts[1] = %d, want 0", c[1])
	}
}

func TestDisjoint(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]string
		want bool
	}{
		{"shared key, different values", map[string]string{"role": "gw"}, map[string]string{"role": "dash"}, true},
		{"shared key, same value", map[string]string{"role": "gw"}, map[string]string{"role": "gw"}, false},
		{"no shared key", map[string]string{"app": "gw"}, map[string]string{"tier": "web"}, false},
		{"one is a superset", map[string]string{"app": "gw"}, map[string]string{"app": "gw", "tier": "web"}, false},
		{"shared key differs among several", map[string]string{"app": "x", "role": "gw"}, map[string]string{"app": "x", "role": "dash"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := disjoint(c.a, c.b); got != c.want {
				t.Errorf("disjoint = %v, want %v", got, c.want)
			}
		})
	}
}
```

Add the two shared test helpers at the bottom of the same file:

```go
// errorsAs is errors.As, wrapped so the test file reads without the import
// sitting next to every assertion.
func errorsAs(err error, target **rejection) bool { return errors.As(err, target) }

// assertNoWrites fails if the fake clientset recorded anything but reads.
// "Nothing was written" is half of AC-03 and AC-05.
func assertNoWrites(t *testing.T, r *Reconciler) {
	t.Helper()
	for _, a := range r.Kube.(*fake.Clientset).Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("unexpected write: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}
```

Add `"errors"` to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/controller/ -v`
Expected: FAIL — build error, `undefined: Reconciler`, `undefined: rejection`, `undefined: disjoint`.

- [ ] **Step 3: Write the implementation**

Create `pkg/controller/validate.go`:

```go
package controller

import (
	"context"
	"fmt"
	"slices"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// rejection is a precondition failure. Retrying will not help until the cluster
// or the spec changes, so it becomes a Rejected status rather than a requeue.
// Any other error is an API failure and is retried.
type rejection struct{ reason string }

func (e *rejection) Error() string { return e.reason }

func reject(format string, args ...any) error {
	return &rejection{reason: fmt.Sprintf(format, args...)}
}

// counts is how many pods each peer currently matches, index-aligned with
// spec.peers.
type counts []int

// validate checks every precondition in BR-03, BR-04 and BR-05 before anything
// is written, and returns the matched pod count per peer.
func (r *Reconciler) validate(ctx context.Context, iso *v1alpha1.NetworkIsolation) (counts, error) {
	peers := iso.Spec.Peers

	// The CRD pins this, but an object stored before the constraint tightened,
	// or one that reached etcd another way, must not reach the policy builder.
	if len(peers) != 2 {
		return nil, reject("spec.peers holds %d entries; exactly two are required", len(peers))
	}

	// BR-05, checked before any API call: never target a protected namespace.
	for _, g := range peers {
		if r.Protected[g.Namespace] {
			return nil, reject("namespace %q is protected and may not be targeted", g.Namespace)
		}
	}

	// BR-04: two peers in one namespace must be provably disjoint, or one
	// policy's complement would block the other peer's own pods.
	if peers[0].Namespace == peers[1].Namespace &&
		!disjoint(peers[0].PodSelector.MatchLabels, peers[1].PodSelector.MatchLabels) {
		return nil, reject(
			"both peers target namespace %q but their selectors are not provably disjoint: no label key is held by both with different values",
			peers[0].Namespace)
	}

	out := make(counts, len(peers))
	checked := map[string]bool{}
	for i, g := range peers {
		if _, err := r.Kube.CoreV1().Namespaces().Get(ctx, g.Namespace, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, reject("namespace %q does not exist", g.Namespace)
			}
			return nil, err
		}

		n, err := r.countPods(ctx, g)
		if err != nil {
			return nil, err
		}
		out[i] = n

		// One namespace, one foreign-policy scan, even when both peers share it.
		if checked[g.Namespace] {
			continue
		}
		checked[g.Namespace] = true
		if err := r.checkForeignPolicies(ctx, g.Namespace, string(iso.UID)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// disjoint reports whether two equality selectors can never match the same pod:
// they share a key and disagree on its value (BR-04). A superset relationship
// is not disjoint — a pod can satisfy both.
func disjoint(a, b map[string]string) bool {
	for k, va := range a {
		if vb, ok := b[k]; ok && vb != va {
			return true
		}
	}
	return false
}

// countPods returns how many pods g currently matches, rejecting if any of them
// runs on the host network, where NetworkPolicy behaviour is undefined (BR-04).
func (r *Reconciler) countPods(ctx context.Context, g v1alpha1.Group) (int, error) {
	list, err := r.Kube.CoreV1().Pods(g.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(g.PodSelector.MatchLabels).String(),
	})
	if err != nil {
		return 0, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.HostNetwork {
			return 0, reject("pod %s/%s matches the selector and uses hostNetwork, for which NetworkPolicy behaviour is undefined upstream",
				g.Namespace, list.Items[i].Name)
		}
	}
	return len(list.Items), nil
}

// checkForeignPolicies refuses to operate in a namespace holding any
// ingress-affecting policy this operation does not own. NetworkPolicy is
// additive: an allow rule placed beside an existing ingress policy widens
// access instead of narrowing it (BR-03).
func (r *Reconciler) checkForeignPolicies(ctx context.Context, namespace, uid string) error {
	list, err := r.Kube.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range list.Items {
		p := &list.Items[i]
		if p.Labels[v1alpha1.OperationLabel] == uid {
			continue
		}
		if affectsIngress(p) {
			return reject("namespace %q already contains ingress policy %q, which this operation does not own", namespace, p.Name)
		}
	}
	return nil
}

// affectsIngress applies the policyTypes defaulting rule: an omitted or empty
// policyTypes always implies Ingress, so only an explicit egress-only list is
// safe to ignore.
func affectsIngress(p *networkingv1.NetworkPolicy) bool {
	if len(p.Spec.PolicyTypes) == 0 {
		return true
	}
	return slices.Contains(p.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
}
```

Add the `Reconciler` struct at the top of `pkg/controller/validate.go` for now; Task 5 moves it to `reconcile.go` with no field changes:

```go
// Reconciler carries everything one reconcile pass needs. It holds client
// interfaces rather than concrete clients, so both fakes drive it directly.
type Reconciler struct {
	Kube      kubernetes.Interface
	Dyn       dynamic.Interface
	Protected map[string]bool
	Timeout   time.Duration
	Now       func() time.Time
}
```

with imports `"time"`, `"k8s.io/client-go/dynamic"`, `"k8s.io/client-go/kubernetes"`.

- [ ] **Step 4: Vendor the new imports**

Run: `go mod vendor`
Expected: `vendor/k8s.io/client-go/dynamic` and `vendor/k8s.io/client-go/kubernetes/fake` appear. No change to `go.mod`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/controller/ -v`
Expected: PASS, all sub-tests.

- [ ] **Step 6: Commit**

```bash
git add pkg/controller vendor
git commit -m "feat(controller): validate preconditions before any write

Covers AC-03 (foreign ingress policies, policyTypes defaulting) and AC-05
(protected namespace, missing namespace, overlapping selectors, hostNetwork)."
```

---

### Task 5: Activation (AC-06, AC-08)

Finalizer, policy writes, status. Nothing here may write a policy before the finalizer is durable, and nothing may roll back a successful write.

**Files:**

- Create: `pkg/controller/reconcile.go`
- Modify: `pkg/controller/validate.go` (move the `Reconciler` struct out of it)
- Test: `pkg/controller/reconcile_test.go`

**Interfaces:**

- Consumes: `Reconciler`, `rejection`, `counts`, `validate` (Task 4); `policy.Build`, `policy.Names` (Task 2); `v1alpha1.*` (Task 1).
- Produces:
  - `func (r *Reconciler) Reconcile(ctx context.Context, key string) error` — `key` is `"namespace/name"` of the NetworkIsolation
  - `func (r *Reconciler) activate(ctx context.Context, logger klog.Logger, iso *v1alpha1.NetworkIsolation) error`
  - `func (r *Reconciler) applyPolicy(ctx context.Context, want *networkingv1.NetworkPolicy, uid string) error`
  - `func (r *Reconciler) setStatus(ctx context.Context, iso *v1alpha1.NetworkIsolation, want v1alpha1.Status) error`
  - `func (r *Reconciler) update(ctx context.Context, iso *v1alpha1.NetworkIsolation) error`

- [ ] **Step 1: Move the Reconciler struct**

Cut the `Reconciler` struct (and its `time`/`dynamic`/`kubernetes` imports) out of `validate.go` and paste it at the top of the new `pkg/controller/reconcile.go`. `validate.go` keeps its methods.

- [ ] **Step 2: Write the failing happy-path test**

Create `pkg/controller/reconcile_test.go`:

```go
package controller

import (
	"context"
	"errors"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/policy"
)

// dynClient builds a fake dynamic client holding iso. The custom list kind is
// required: the fake cannot infer a list kind for an unregistered CRD.
func dynClient(t *testing.T, iso *v1alpha1.NetworkIsolation) *dynamicfake.FakeDynamicClient {
	t.Helper()
	u, err := v1alpha1.ToUnstructured(iso)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
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

// A clean activation: finalizer first, then both policies, then Active with
// the matched counts.
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
	if len(got.Status.Peers) != 2 {
		t.Fatalf("status.peers = %v, want 2 entries", got.Status.Peers)
	}
	if got.Status.LastReconcileTime == "" {
		t.Error("lastReconcileTime not set")
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != v1alpha1.Finalizer {
		t.Errorf("finalizers = %v", got.Finalizers)
	}

	names := policy.Names(uid)
	for i, want := range []v1alpha1.PeerStatus{{Policy: names[0], Matched: 1}, {Policy: names[1], Matched: 0}} {
		if got.Status.Peers[i] != want {
			t.Errorf("status.peers[%d] = %+v, want %+v", i, got.Status.Peers[i], want)
		}
	}
	for _, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		np, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("policy %s/%s: %v", p.ns, p.name, err)
		}
		if np.Labels[v1alpha1.OperationLabel] != uid {
			t.Errorf("policy %s missing the operation label", p.name)
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
	r.Dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("update", "networkisolations", func(a k8stesting.Action) (bool, runtime.Object, error) {
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
```

Add `"k8s.io/client-go/kubernetes/fake"` to this file's imports.

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./pkg/controller/ -run 'TestActivate|TestFinalizer' -v`
Expected: FAIL — `undefined: (*Reconciler).Reconcile`.

- [ ] **Step 4: Write the implementation**

Create `pkg/controller/reconcile.go`:

```go
package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/policy"
)

// Reconciler carries everything one reconcile pass needs. It holds client
// interfaces rather than concrete clients, so both fakes drive it directly.
type Reconciler struct {
	Kube      kubernetes.Interface
	Dyn       dynamic.Interface
	Protected map[string]bool
	Timeout   time.Duration
	Now       func() time.Time
}

// Reconcile drives one NetworkIsolation, named by its "namespace/name" key,
// towards what its spec asks for. Desired state is re-derived from the spec on
// every pass, so a crash between writes is repaired by the next one (FR-04).
func (r *Reconciler) Reconcile(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	u, err := r.Dyn.Resource(v1alpha1.Resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Gone, and the finalizer guaranteed cleanup ran before it went.
		return nil
	}
	if err != nil {
		return err
	}
	iso, err := v1alpha1.FromUnstructured(u)
	if err != nil {
		return err
	}

	logger := klog.FromContext(ctx).WithValues("operation", string(iso.UID), "object", key)

	// Deletion always takes precedence over activation, so a restart
	// mid-deletion resumes rather than recreating policies (FR-04).
	if iso.DeletionTimestamp != nil {
		return r.cleanup(ctx, logger, iso)
	}
	return r.activate(ctx, logger, iso)
}

// activate validates, persists the finalizer, writes both policies and reports.
func (r *Reconciler) activate(ctx context.Context, logger klog.Logger, iso *v1alpha1.NetworkIsolation) error {
	c, err := r.validate(ctx, iso)
	var rej *rejection
	if errors.As(err, &rej) {
		// Before activation, a failed precondition means nothing was written:
		// Rejected. After it — the finalizer is the marker — the policies are
		// already in place, they stay there, and it is the assumption behind
		// them that no longer holds: Degraded, retried until it holds again.
		phase := v1alpha1.PhaseRejected
		if slices.Contains(iso.Finalizers, v1alpha1.Finalizer) {
			phase = v1alpha1.PhaseDegraded
		}
		logger.Info(string(phase), "reason", rej.reason)
		if err := r.setStatus(ctx, iso, v1alpha1.Status{
			Phase:    phase,
			Message:  rej.reason,
			Peers: iso.Status.Peers,
		}); err != nil {
			return err
		}
		if phase == v1alpha1.PhaseDegraded {
			return fmt.Errorf("precondition no longer holds: %s", rej.reason)
		}
		return nil
	}
	if err != nil {
		return err
	}

	// Nothing is written until cleanup is guaranteed a chance to run (FR-03).
	if !slices.Contains(iso.Finalizers, v1alpha1.Finalizer) {
		iso.Finalizers = append(iso.Finalizers, v1alpha1.Finalizer)
		if err := r.update(ctx, iso); err != nil {
			return err
		}
	}

	desired := policy.Build(iso)
	peers := make([]v1alpha1.PeerStatus, len(desired))
	var failures []string
	for i, want := range desired {
		peers[i] = v1alpha1.PeerStatus{Policy: want.Name, Matched: c[i]}
		if err := r.applyPolicy(ctx, want, string(iso.UID)); err != nil {
			// Whatever succeeded stays: rolling back would reopen traffic
			// this operation was asked to block (FR-04).
			logger.Error(err, "Policy write failed", "policy", want.Namespace+"/"+want.Name)
			failures = append(failures, fmt.Sprintf("%s/%s: %v", want.Namespace, want.Name, err))
		}
	}

	status := v1alpha1.Status{Phase: v1alpha1.PhaseActive, Peers: peers}
	if len(failures) > 0 {
		status.Phase = v1alpha1.PhaseDegraded
		status.Message = strings.Join(failures, "; ")
	}
	if err := r.setStatus(ctx, iso, status); err != nil {
		return err
	}
	if len(failures) > 0 {
		// Returned so the queue retries; the status already says why.
		return fmt.Errorf("%d of %d policies not written", len(failures), len(desired))
	}
	logger.Info("Active", "peers", peers)
	return nil
}

// applyPolicy creates or updates one owned policy. A policy carrying someone
// else's operation label under the same name is reported, never overwritten
// (FR-03). When the stored spec already matches, no write is issued at all, so
// a steady state costs nothing (AC-06).
func (r *Reconciler) applyPolicy(ctx context.Context, want *networkingv1.NetworkPolicy, uid string) error {
	api := r.Kube.NetworkingV1().NetworkPolicies(want.Namespace)
	got, err := api.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, want, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if got.Labels[v1alpha1.OperationLabel] != uid {
		return fmt.Errorf("name is occupied by policy %s/%s, which this operation does not own", want.Namespace, want.Name)
	}
	if equality.Semantic.DeepEqual(got.Spec, want.Spec) {
		return nil
	}
	got.Spec = want.Spec
	got.Labels = want.Labels
	got.Annotations = want.Annotations
	_, err = api.Update(ctx, got, metav1.UpdateOptions{})
	return err
}

// setStatus writes status only when something other than the timestamp
// changed, so reconciling an unchanged object produces no writes at all
// (AC-06). lastReconcileTime therefore records when the observation last
// changed, not when the last pass ran.
func (r *Reconciler) setStatus(ctx context.Context, iso *v1alpha1.NetworkIsolation, want v1alpha1.Status) error {
	want.LastReconcileTime = iso.Status.LastReconcileTime
	if equality.Semantic.DeepEqual(iso.Status, want) {
		return nil
	}
	want.LastReconcileTime = r.Now().UTC().Format(time.RFC3339)
	iso.Status = want

	u, err := v1alpha1.ToUnstructured(iso)
	if err != nil {
		return err
	}
	out, err := r.Dyn.Resource(v1alpha1.Resource).Namespace(iso.Namespace).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return refresh(iso, out)
}

// update persists a metadata change — only ever the finalizer list — and
// refreshes iso with the stored version, so the status write that follows in
// the same pass is not a conflict.
func (r *Reconciler) update(ctx context.Context, iso *v1alpha1.NetworkIsolation) error {
	u, err := v1alpha1.ToUnstructured(iso)
	if err != nil {
		return err
	}
	out, err := r.Dyn.Resource(v1alpha1.Resource).Namespace(iso.Namespace).Update(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return refresh(iso, out)
}

func refresh(iso *v1alpha1.NetworkIsolation, from *unstructured.Unstructured) error {
	fresh, err := v1alpha1.FromUnstructured(from)
	if err != nil {
		return err
	}
	*iso = *fresh
	return nil
}
```

Add `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"` to the imports.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/controller/ -run 'TestActivate|TestFinalizer' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/controller
git commit -m "feat(controller): activation writes the finalizer then both policies"
```

- [ ] **Step 7: Write the idempotency test (AC-06)**

Append to `pkg/controller/reconcile_test.go`:

```go
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
	if got := stored(t, r, object); got.Status.Phase != v1alpha1.PhaseActive {
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
	for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("unexpected write to the custom resource: %s %s", a.GetVerb(), a.GetSubresource())
		}
	}
}
```

- [ ] **Step 8: Run it**

Run: `go test ./pkg/controller/ -run TestReconcileIsIdempotent -v`
Expected: PASS. A failure here usually means `policy.complement` iterates the label map without sorting, so the generated spec differs between passes — fix the sort, not the comparison.

- [ ] **Step 9: Write the partial-write test (AC-08)**

Append to `pkg/controller/reconcile_test.go`:

```go
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
		create := a.(k8stesting.CreateAction)
		np := create.GetObject().(*networkingv1.NetworkPolicy)
		if failing && np.Name == names[1] {
			return true, nil, apierrors.NewInternalError(errors.New("etcd is unhappy"))
		}
		return false, nil, nil
	})

	err := r.Reconcile(context.Background(), key(object))
	if err == nil {
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
	for _, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		if _, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{}); err != nil {
			t.Errorf("policy %s/%s missing after convergence: %v", p.ns, p.name, err)
		}
	}
}
```

Add `"strings"` to this file's imports.

- [ ] **Step 10: Run it**

Run: `go test ./pkg/controller/ -run TestPartialWrite -v`
Expected: PASS.

- [ ] **Step 11: Write the post-activation degradation test**

The spec's error table separates two readings of the same precondition failure:
before activation nothing was written, so it is `Rejected`; after activation the
policies exist and stay, so it is `Degraded`. Append to
`pkg/controller/reconcile_test.go`:

```go
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
	for _, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		if _, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{}); err != nil {
			t.Errorf("owned policy %s/%s was removed: %v", p.ns, p.name, err)
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
```

- [ ] **Step 12: Run them**

Run: `go test ./pkg/controller/ -run 'TestForeignPolicyAfterActivation|TestRejectionBeforeActivation' -v`
Expected: PASS.

- [ ] **Step 13: Write the Terminating-namespace test (Review Focus 1)**

Append to `pkg/controller/reconcile_test.go`:

```go
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
```

- [ ] **Step 14: Run it**

Run: `go test ./pkg/controller/ -run TestTerminatingNamespace -v`
Expected: PASS.

- [ ] **Step 15: Write the stale-resourceVersion test (Review Focus 3)**

Append to `pkg/controller/reconcile_test.go`:

```go
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
```

- [ ] **Step 16: Run the whole package**

Run: `go test ./pkg/controller/ -race -cover -v`
Expected: PASS, coverage ≥90%.

- [ ] **Step 17: Commit**

```bash
git add pkg/controller
git commit -m "test(controller): idempotency, partial writes, terminating namespace, conflicts

Covers AC-06 and AC-08 plus the resourceVersion and Terminating cases the
acceptance criteria do not name."
```

---

### Task 6: Deletion and cleanup (AC-04)

Deletion has to work when nothing else does: namespaces gone, pods gone, preconditions long since false.

**Files:**

- Modify: `pkg/controller/reconcile.go`
- Test: `pkg/controller/reconcile_test.go`

**Interfaces:**

- Consumes: `Reconciler`, `setStatus`, `update` (Task 5); `policy.Names` (Task 2).
- Produces: `func (r *Reconciler) cleanup(ctx context.Context, logger klog.Logger, iso *v1alpha1.NetworkIsolation) error`, `func (r *Reconciler) deletePolicy(ctx context.Context, namespace, name, uid string) error`.

- [ ] **Step 1: Write the failing deletion tests**

Append to `pkg/controller/reconcile_test.go`:

```go
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

	for _, p := range []struct{ ns, name string }{{"tenant-a", names[0]}, {"tenant-b", names[1]}} {
		if _, err := r.Kube.NetworkingV1().NetworkPolicies(p.ns).Get(context.Background(), p.name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("policy %s/%s still present (err %v)", p.ns, p.name, err)
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
	if _, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-a").Get(context.Background(), names[0], metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("surviving policy %s not removed", names[0])
	}
	if _, err := r.Kube.NetworkingV1().NetworkPolicies("tenant-b").Get(context.Background(), names[1], metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("policy %s reappeared", names[1])
	}
}

// A policy occupying one of our names but owned by someone else is left alone,
// even during cleanup (FR-03).
func TestCleanupLeavesForeignPolicyAlone(t *testing.T) {
	object := deleting()
	names[0], _ := policy.Names(uid)
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./pkg/controller/ -run TestCleanup -v`
Expected: FAIL — `undefined: (*Reconciler).cleanup`.

- [ ] **Step 3: Write the implementation**

Append to `pkg/controller/reconcile.go`:

```go
// cleanup removes the policies this operation owns, then the finalizer. It must
// not depend on matching pods, namespaces or preconditions still being valid
// (FR-03). Cross-namespace owner references do not work, so this is explicit.
func (r *Reconciler) cleanup(ctx context.Context, logger klog.Logger, iso *v1alpha1.NetworkIsolation) error {
	if !slices.Contains(iso.Finalizers, v1alpha1.Finalizer) {
		// Nothing was ever written under this operation, so there is nothing
		// to undo and nothing holding the object back.
		return nil
	}

	if err := r.setStatus(ctx, iso, v1alpha1.Status{
		Phase:    v1alpha1.PhaseDeleting,
		Peers: iso.Status.Peers,
	}); err != nil {
		return err
	}

	names[0], names[1] := policy.Names(string(iso.UID))
	targets := []struct{ namespace, name string }{
		{iso.Spec.Peers[0].Namespace, names[0]},
		{iso.Spec.Peers[1].Namespace, names[1]},
	}
	for _, t := range targets {
		if err := r.deletePolicy(ctx, t.namespace, t.name, string(iso.UID)); err != nil {
			// An API error is not evidence the policy is gone. Keep the
			// finalizer and retry (FR-03).
			logger.Error(err, "Cleanup incomplete, finalizer retained", "policy", t.namespace+"/"+t.name)
			return err
		}
	}

	iso.Finalizers = slices.DeleteFunc(iso.Finalizers, func(f string) bool { return f == v1alpha1.Finalizer })
	logger.Info("Cleaned up", "policies", []string{names[0], names[1]})
	return r.update(ctx, iso)
}

// deletePolicy removes one policy if this operation owns it. An already absent
// policy is success; a policy under the same name owned by anyone else is left
// untouched.
func (r *Reconciler) deletePolicy(ctx context.Context, namespace, name, uid string) error {
	api := r.Kube.NetworkingV1().NetworkPolicies(namespace)
	got, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if got.Labels[v1alpha1.OperationLabel] != uid {
		return nil
	}
	if err := api.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./pkg/controller/ -run TestCleanup -v`
Expected: PASS, all five.

- [ ] **Step 5: Commit**

```bash
git add pkg/controller
git commit -m "feat(controller): cleanup removes owned policies then the finalizer

Covers AC-04: tolerates absent policies, never treats an API error as
absence, never recreates during deletion, never touches foreign policies."
```

- [ ] **Step 6: Write the no-finalizer deletion test (Review Focus 2)**

Append to `pkg/controller/reconcile_test.go`:

```go
// Review Focus 2: an object that was Rejected never got a finalizer. Deleting
// it must be a no-op — no status write, no attempt to strip a finalizer that
// isn't there, no API calls that would fail against a missing namespace.
func TestCleanupWithoutFinalizerIsANoOp(t *testing.T) {
	object := iso("tenant-a", map[string]string{"app": "gateway"}, "tenant-b", map[string]string{"app": "dashboard"})
	now := metav1.Now()
	object.DeletionTimestamp = &now
	object.Status = v1alpha1.Status{Phase: v1alpha1.PhaseRejected, Message: "namespace \"tenant-b\" does not exist"}

	r := newReconciler()
	r.Dyn = dynClient(t, object)

	if err := r.Reconcile(context.Background(), key(object)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertNoWrites(t, r)
	for _, a := range r.Dyn.(*dynamicfake.FakeDynamicClient).Actions() {
		switch a.GetVerb() {
		case "get", "list", "watch":
		default:
			t.Errorf("unexpected write during a no-op deletion: %s %s", a.GetVerb(), a.GetSubresource())
		}
	}
}
```

- [ ] **Step 7: Run it and check package coverage**

Run: `go test ./pkg/... -race -cover -v`
Expected: PASS everywhere, `pkg/controller` and `pkg/policy` both ≥90%.

- [ ] **Step 8: Commit**

```bash
git add pkg/controller
git commit -m "test(controller): deleting a never-activated object is a no-op"
```

---

### Task 7: Informers, workqueue and the binary

The reconciler is finished and tested. This is the plumbing that calls it.

**Files:**

- Create: `pkg/controller/controller.go`, `cmd/main/main.go`
- Test: `pkg/controller/controller_test.go`

**Interfaces:**

- Consumes: `Reconciler.Reconcile` (Task 5), `v1alpha1.Resource`, `v1alpha1.OperationLabel`, `v1alpha1.OwnerAnnotation` (Task 1).
- Produces:
  - `func New(kube kubernetes.Interface, dyn dynamic.Interface, r *Reconciler, resync time.Duration) *Controller`
  - `func (c *Controller) Run(ctx context.Context) error`
  - `func (c *Controller) enqueuePolicy(obj any)` — unexported, exercised by the test through `New`

- [ ] **Step 1: Write the failing test**

Create `pkg/controller/controller_test.go`:

```go
package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/controller/ -run TestOwnerKey -v`
Expected: FAIL — `undefined: ownerKey`.

- [ ] **Step 3: Write the plumbing**

Create `pkg/controller/controller.go`:

```go
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
func New(kube kubernetes.Interface, dyn dynamic.Interface, r *Reconciler, resync time.Duration) *Controller {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		reconcile: r.Reconcile,
	}

	isoFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, resync, metav1.NamespaceAll, nil)
	isoInformer := isoFactory.ForResource(v1alpha1.Resource).Informer()
	// Errors here would mean the informer could not accept a handler, which is
	// a programming error rather than a runtime condition.
	if _, err := isoInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.enqueue(obj) },
		UpdateFunc: func(_, obj any) { c.enqueue(obj) },
		DeleteFunc: func(obj any) { c.enqueue(obj) },
	}); err != nil {
		panic(err)
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
		panic(err)
	}

	c.start = []func(<-chan struct{}){isoFactory.Start, polFactory.Start}
	c.synced = []cache.InformerSynced{isoInformer.HasSynced, polInformer.HasSynced}
	return c
}

// Run starts the informers and a single worker. One worker is enough: the
// queue already serialises a key, and FR-04 assumes a single writer.
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
	meta, err := metaAccessor(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if key, ok := ownerKey(meta); ok {
		c.queue.Add(key)
	}
}

func metaAccessor(obj any) (metav1.Object, error) {
	m, ok := obj.(metav1.Object)
	if !ok {
		return nil, errors.New("object has no metadata")
	}
	return m, nil
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
```

- [ ] **Step 4: Vendor and run**

```bash
go mod vendor
go test ./pkg/controller/ -run TestOwnerKey -v
```

Expected: `vendor/k8s.io/client-go/informers` and `vendor/k8s.io/client-go/dynamic/dynamicinformer` appear; the test PASSes.

- [ ] **Step 5: Write the binary**

Create `cmd/main/main.go`:

```go
// Command main runs the network-isolation controller.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
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
	logger.Info("Starting", "protectedNamespaces", keys(protected), "resync", *resync)

	r := &controller.Reconciler{
		Kube:      kube,
		Dyn:       dyn,
		Protected: protected,
		Timeout:   *timeout,
		Now:       time.Now,
	}
	if err := controller.New(kube, dyn, r, *resync).Run(ctx); err != nil {
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

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

`Reconciler`'s fields are already exported, so `cmd` can construct it. If the compiler disagrees, the struct in `pkg/controller/reconcile.go` was written with unexported fields — fix it there.

- [ ] **Step 6: Build and run against the kind cluster**

```bash
go build ./...
go run ./cmd/main -kubeconfig ~/.kube/config -v=2
```

Expected: `Starting NetworkIsolation controller` and no errors. `Ctrl-C` exits cleanly. If it reports an RBAC error, that is expected here — you are running as your own kubeconfig user, which has cluster-admin on kind.

- [ ] **Step 7: Commit**

```bash
go mod vendor
git add cmd pkg vendor
git commit -m "feat(controller): informers, workqueue and the controller binary"
```

---

### Task 8: Deployment manifests, samples and image

**Files:**

- Create: `deploy/rbac.yaml`, `deploy/controller.yaml`, `deploy/samples/workloads.yaml`, `deploy/samples/isolation.yaml`
- Modify: `Dockerfile`, `Makefile`

**Interfaces:**

- Consumes: the flags and `POD_NAMESPACE` from `cmd/main/main.go` (Task 7); `deploy/crd.yaml` (Task 3).
- Produces: `make image`, `make deploy`; namespaces `tenant-a`, `tenant-b`, `tenant-c` with probe pods for Task 9.

- [ ] **Step 1: Fix the Dockerfile**

The current one names no build stage but copies `--from=builder`, so it cannot build. The repository vendors its dependencies, so `go mod download` is dead weight too. Replace `Dockerfile` entirely:

```dockerfile
FROM golang:1.27 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -ldflags="-s -w" -o /main ./cmd/main

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /main /main
USER nonroot:nonroot
ENTRYPOINT ["/main"]
```

- [ ] **Step 2: Verify the image builds**

```bash
docker build -t ghcr.io/joaopaulosr95/k8s-workload-hardening:dev .
```

Expected: a successful build. A `--from=builder` error means Step 1 was not applied.

- [ ] **Step 3: Write the RBAC**

Create `deploy/rbac.yaml`:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: isolation-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: network-isolation
  namespace: isolation-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: network-isolation
rules:
  # Target namespaces are chosen at runtime, so this cannot be namespace-scoped.
  - apiGroups: ["networking.k8s.io"]
    resources: ["networkpolicies"]
    verbs: ["create", "get", "list", "watch", "update", "delete"]
  # Read-only: membership, existence and hostNetwork checks. No workload reads,
  # no exec, no secrets.
  - apiGroups: [""]
    resources: ["pods", "namespaces"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["hardening.k8s.io"]
    resources: ["networkisolations"]
    verbs: ["get", "list", "watch", "update"]
  - apiGroups: ["hardening.k8s.io"]
    resources: ["networkisolations/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: ["hardening.k8s.io"]
    resources: ["networkisolations/finalizers"]
    verbs: ["update"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: network-isolation
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: network-isolation
subjects:
  - kind: ServiceAccount
    name: network-isolation
    namespace: isolation-system
```

- [ ] **Step 4: Write the Deployment**

Create `deploy/controller.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: network-isolation
  namespace: isolation-system
  labels: { app: network-isolation }
spec:
  # One replica. FR-04 states single-writer exclusivity as an operational
  # precondition; there is no leader election yet (G-01).
  replicas: 1
  strategy: { type: Recreate }
  selector:
    matchLabels: { app: network-isolation }
  template:
    metadata:
      labels: { app: network-isolation }
    spec:
      serviceAccountName: network-isolation
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: controller
          image: ghcr.io/joaopaulosr95/k8s-workload-hardening:dev
          imagePullPolicy: IfNotPresent
          args: ["-v=2", "-resync=30s"]
          env:
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef: { fieldPath: metadata.namespace }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits: { memory: 128Mi }
```

- [ ] **Step 5: Write the sample workloads**

Create `deploy/samples/workloads.yaml`. Every pod runs the same busybox probe: `httpd` for TCP and an `nc -u` echo loop for UDP, so one image covers both protocols with no registry beyond Docker Hub's busybox.

```yaml
apiVersion: v1
kind: Namespace
metadata: { name: tenant-a }
---
apiVersion: v1
kind: Namespace
metadata: { name: tenant-b }
---
apiVersion: v1
kind: Namespace
metadata: { name: tenant-c }
---
# gateway: group A
apiVersion: v1
kind: Pod
metadata:
  name: gateway
  namespace: tenant-a
  labels: { app: gateway }
spec:
  containers:
    - name: probe
      image: busybox:1.37
      command: ["/bin/sh", "-c"]
      args:
        - |
          mkdir -p /srv && hostname > /srv/index.html
          httpd -f -p 8080 -h /srv &
          while true; do hostname | nc -u -l -p 8081 -w 1 >/dev/null 2>&1; done
      ports:
        - { containerPort: 8080, protocol: TCP }
        - { containerPort: 8081, protocol: UDP }
---
apiVersion: v1
kind: Service
metadata: { name: gateway, namespace: tenant-a }
spec:
  selector: { app: gateway }
  ports:
    - { name: tcp, port: 8080, targetPort: 8080, protocol: TCP }
    - { name: udp, port: 8081, targetPort: 8081, protocol: UDP }
---
# dashboard: group B
apiVersion: v1
kind: Pod
metadata:
  name: dashboard
  namespace: tenant-b
  labels: { app: dashboard }
spec:
  containers:
    - name: probe
      image: busybox:1.37
      command: ["/bin/sh", "-c"]
      args:
        - |
          mkdir -p /srv && hostname > /srv/index.html
          httpd -f -p 8080 -h /srv &
          while true; do hostname | nc -u -l -p 8081 -w 1 >/dev/null 2>&1; done
      ports:
        - { containerPort: 8080, protocol: TCP }
        - { containerPort: 8081, protocol: UDP }
---
apiVersion: v1
kind: Service
metadata: { name: dashboard, namespace: tenant-b }
spec:
  selector: { app: dashboard }
  ports:
    - { name: tcp, port: 8080, targetPort: 8080, protocol: TCP }
    - { name: udp, port: 8081, targetPort: 8081, protocol: UDP }
---
# bystander: in neither group, must keep talking to both throughout
apiVersion: v1
kind: Pod
metadata:
  name: bystander
  namespace: tenant-c
  labels: { app: bystander }
spec:
  containers:
    - name: probe
      image: busybox:1.37
      command: ["/bin/sh", "-c"]
      args:
        - |
          mkdir -p /srv && hostname > /srv/index.html
          httpd -f -p 8080 -h /srv &
          while true; do hostname | nc -u -l -p 8081 -w 1 >/dev/null 2>&1; done
      ports:
        - { containerPort: 8080, protocol: TCP }
        - { containerPort: 8081, protocol: UDP }
```

Create `deploy/samples/isolation.yaml`:

```yaml
apiVersion: hardening.k8s.io/v1alpha1
kind: NetworkIsolation
metadata:
  name: gateway-dashboard
  namespace: isolation-system
spec:
  a:
    namespace: tenant-a
    podSelector:
      matchLabels: { app: gateway }
  b:
    namespace: tenant-b
    podSelector:
      matchLabels: { app: dashboard }
```

- [ ] **Step 6: Add the Makefile targets**

Replace `Makefile` with:

```make
TAG ?= dev
IMAGE ?= ghcr.io/joaopaulosr95/k8s-workload-hardening
CLUSTER ?= hardening

.PHONY: test cover image kind-up kind-down deploy samples verify verify-crd

test:
	go test ./pkg/... -race

cover:
	go test ./pkg/... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

image:
	docker build -t $(IMAGE):$(TAG) .

kind-up:
	kind create cluster --name $(CLUSTER) --config hack/kind/cluster.yaml

kind-down:
	kind delete cluster --name $(CLUSTER)

deploy: image
	kind load docker-image $(IMAGE):$(TAG) --name $(CLUSTER)
	kubectl apply -f deploy/crd.yaml
	kubectl apply -f deploy/rbac.yaml
	kubectl apply -f deploy/controller.yaml
	kubectl -n isolation-system rollout status deployment/network-isolation --timeout=120s

samples:
	kubectl apply -f deploy/samples/workloads.yaml
	kubectl -n tenant-a wait --for=condition=Ready pod/gateway --timeout=120s
	kubectl -n tenant-b wait --for=condition=Ready pod/dashboard --timeout=120s
	kubectl -n tenant-c wait --for=condition=Ready pod/bystander --timeout=120s

verify: deploy samples
	./hack/verify-isolation.sh

verify-crd:
	./hack/verify-crd.sh
```

- [ ] **Step 7: Deploy and confirm the controller runs**

```bash
make deploy samples
kubectl apply -f deploy/samples/isolation.yaml
kubectl -n isolation-system get netiso gateway-dashboard
```

Expected: `PHASE` is `Active`, `MATCHED-A` and `MATCHED-B` are `1`. Then:

```bash
kubectl -n tenant-a get networkpolicies
kubectl -n tenant-b get networkpolicies
```

Expected: one `netiso-<uid>-0` in `tenant-a` and one `netiso-<uid>-1` in `tenant-b`. If the phase is `Rejected`, read `.status.message` — it names the precondition that failed.

- [ ] **Step 8: Confirm deletion restores the cluster**

```bash
kubectl delete -f deploy/samples/isolation.yaml
kubectl -n tenant-a get networkpolicies
kubectl -n tenant-b get networkpolicies
```

Expected: `delete` returns promptly (it does not hang on the finalizer), and both namespaces report `No resources found`.

- [ ] **Step 9: Commit**

```bash
git add Dockerfile Makefile deploy
git commit -m "feat(deploy): CRD, RBAC, Deployment, sample workloads and Makefile

Fixes the Dockerfile's missing builder stage and drops go mod download,
since the repository vendors its dependencies."
```

---

### Task 9: Live traffic verification (AC-07) and README

The only evidence of packet enforcement. Fake-client tests are not.

**Files:**

- Create: `hack/verify-isolation.sh`
- Modify: `README.md`

**Interfaces:**

- Consumes: `deploy/samples/workloads.yaml`, `deploy/samples/isolation.yaml`, `make deploy samples` (Task 8).
- Produces: `make verify`.

- [ ] **Step 1: Write the verification script**

Create `hack/verify-isolation.sh`:

```bash
#!/usr/bin/env bash
# AC-07: on a live kind cluster, TCP and UDP traffic between the two groups —
# over pod IP and ClusterIP — works before isolation, fails after enforcement
# converges, and works again after the object is deleted. DNS and traffic to an
# unrelated pod are unaffected throughout.
set -euo pipefail

iso=deploy/samples/isolation.yaml
converge_timeout=${CONVERGE_TIMEOUT:-60}
fail=0

# probe <src-ns> <src-pod> <proto> <host> — 0 if the reply came back.
probe() {
  local ns=$1 pod=$2 proto=$3 host=$4 out
  if [ "$proto" = tcp ]; then
    out=$(kubectl exec -n "$ns" "$pod" -- sh -c "wget -q -T 3 -O - http://$host:8080/ 2>/dev/null" 2>/dev/null || true)
  else
    out=$(kubectl exec -n "$ns" "$pod" -- sh -c "echo probe | nc -u -w 3 $host 8081 2>/dev/null" 2>/dev/null || true)
  fi
  [ -n "$out" ]
}

check() {
  local want=$1 desc=$2; shift 2
  if "$@"; then got=reachable; else got=blocked; fi
  if [ "$got" = "$want" ]; then
    echo "ok    $desc: $got"
  else
    echo "FAIL  $desc: $got, want $want"
    fail=1
  fi
}

# wait_for <reachable|blocked> <probe args...> — poll until enforcement converges.
wait_for() {
  local want=$1; shift
  local deadline=$((SECONDS + converge_timeout))
  while [ $SECONDS -lt $deadline ]; do
    if "$@"; then got=reachable; else got=blocked; fi
    [ "$got" = "$want" ] && return 0
    sleep 2
  done
  return 1
}

gwIP=$(kubectl -n tenant-a get pod gateway -o jsonpath='{.status.podIP}')
dashIP=$(kubectl -n tenant-b get pod dashboard -o jsonpath='{.status.podIP}')
bystanderIP=$(kubectl -n tenant-c get pod bystander -o jsonpath='{.status.podIP}')
gwSvc=gateway.tenant-a.svc.cluster.local
dashSvc=dashboard.tenant-b.svc.cluster.local

echo "== before isolation =="
check reachable "A->B pod IP   TCP" probe tenant-a gateway   tcp "$dashIP"
check reachable "A->B pod IP   UDP" probe tenant-a gateway   udp "$dashIP"
check reachable "B->A pod IP   TCP" probe tenant-b dashboard tcp "$gwIP"
check reachable "B->A pod IP   UDP" probe tenant-b dashboard udp "$gwIP"
check reachable "A->B ClusterIP TCP" probe tenant-a gateway   tcp "$dashSvc"
check reachable "B->A ClusterIP TCP" probe tenant-b dashboard tcp "$gwSvc"
check reachable "A->bystander  TCP" probe tenant-a gateway   tcp "$bystanderIP"
check reachable "bystander->A  TCP" probe tenant-c bystander tcp "$gwIP"
check reachable "DNS from A"        kubectl exec -n tenant-a gateway -- nslookup kubernetes.default.svc.cluster.local

echo
echo "== applying isolation =="
kubectl apply -f "$iso" >/dev/null
kubectl -n isolation-system wait --for=jsonpath='{.status.phase}'=Active netiso/gateway-dashboard --timeout=60s
if ! wait_for blocked probe tenant-a gateway tcp "$dashIP"; then
  echo "FAIL  enforcement did not converge within ${converge_timeout}s"
  fail=1
fi

echo "== after isolation =="
check blocked   "A->B pod IP   TCP" probe tenant-a gateway   tcp "$dashIP"
check blocked   "A->B pod IP   UDP" probe tenant-a gateway   udp "$dashIP"
check blocked   "B->A pod IP   TCP" probe tenant-b dashboard tcp "$gwIP"
check blocked   "B->A pod IP   UDP" probe tenant-b dashboard udp "$gwIP"
check blocked   "A->B ClusterIP TCP" probe tenant-a gateway   tcp "$dashSvc"
check blocked   "B->A ClusterIP TCP" probe tenant-b dashboard tcp "$gwSvc"
check reachable "A->bystander  TCP" probe tenant-a gateway   tcp "$bystanderIP"
check reachable "bystander->A  TCP" probe tenant-c bystander tcp "$gwIP"
check reachable "bystander->B  TCP" probe tenant-c bystander tcp "$dashIP"
check reachable "DNS from A"        kubectl exec -n tenant-a gateway -- nslookup kubernetes.default.svc.cluster.local
check reachable "DNS from B"        kubectl exec -n tenant-b dashboard -- nslookup kubernetes.default.svc.cluster.local

echo
echo "== removing isolation =="
kubectl delete -f "$iso" --wait=true >/dev/null
if ! wait_for reachable probe tenant-a gateway tcp "$dashIP"; then
  echo "FAIL  connectivity did not return within ${converge_timeout}s"
  fail=1
fi

echo "== after removal =="
check reachable "A->B pod IP   TCP" probe tenant-a gateway   tcp "$dashIP"
check reachable "A->B pod IP   UDP" probe tenant-a gateway   udp "$dashIP"
check reachable "B->A pod IP   TCP" probe tenant-b dashboard tcp "$gwIP"
check reachable "B->A pod IP   UDP" probe tenant-b dashboard udp "$gwIP"
check reachable "A->B ClusterIP TCP" probe tenant-a gateway   tcp "$dashSvc"

left=$(kubectl get networkpolicies -A -l hardening.k8s.io/operation -o name | wc -l | tr -d ' ')
if [ "$left" != "0" ]; then
  echo "FAIL  $left owned policies survived deletion"
  fail=1
else
  echo "ok    no owned policies remain"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "AC-07 FAILED"
  exit 1
fi
echo "AC-07 PASSED"
```

- [ ] **Step 2: Run it**

```bash
chmod +x hack/verify-isolation.sh
make verify
```

Expected: every line `ok`, final `AC-07 PASSED`.

Two things that go wrong here, and what they mean:

- **Everything reads `blocked`, even before isolation.** The probe containers are not serving. Check `kubectl -n tenant-a logs gateway` and that `busybox:1.37` really has `httpd` and `nc -u` (it does; a pull failure looks the same).
- **UDP reads `blocked` before isolation but TCP works.** busybox `nc -u -l` handles one datagram per invocation and the `while` loop may be between iterations. Raise the `-w` timeout, or swap the probe image for `nicolaka/netshoot` and use `socat -u UDP-LISTEN:8081,fork`. Do not weaken the UDP assertion — AC-07 names UDP explicitly.

- [ ] **Step 3: Record the versions actually used**

```bash
kind version; kubectl version --client; go version
kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion}'; echo
```

Keep the output for the README.

- [ ] **Step 4: Write the README**

Replace `README.md`. It must cover, because the assignment and NFR-06 ask for each: setup, build, deploy, run; the decisions taken; the tested versions from Step 3; the limitations from BR-01; and the actual development time.

```markdown
# k8s-workload-hardening

An internal SRE tool for a Kubernetes cluster. Core task 1 — on-demand network
isolation between two workloads — is implemented here.

## What it does

Creating a `NetworkIsolation` object blocks direct network traffic between two
pod groups, each named by a namespace and a label selector. Deleting the object
restores the previous connectivity.

    kubectl apply -f deploy/samples/isolation.yaml
    kubectl -n isolation-system get netiso

## Setup

    make kind-up          # kind cluster, default CNI (enforces NetworkPolicy)
    make deploy           # build, load, install CRD + RBAC + controller
    make samples          # three namespaces with probe pods
    make verify           # AC-07: live TCP/UDP traffic, before/after/removed
    make verify-crd       # AC-09: API-server schema and immutability
    make test cover       # unit tests and coverage

## Tested versions

<paste the Step 3 output: kind, kubectl, kubelet, Go>

## How it works

NetworkPolicy is additive and allow-only: there is no deny rule. A prohibition
is therefore written as _allow everything except the opposing group_. For group
B in namespace N with labels `k1=v1, k2=v2`, the policy protecting A allows
every pod in namespaces other than N, plus, for each of B's requirements, the
pods in N failing it (`k NotIn [v]` — which also matches a pod lacking the key).
The union is exactly "everything that is not B".

That only holds while no other ingress policy in the namespace re-allows what
this one excludes, so the controller refuses to operate in a namespace that
already contains a foreign ingress policy, rather than rewriting policies it
does not own.

## Decisions

- **Plain client-go, no framework.** The brief recommends it and the task needs
  nothing more. The custom resource is read and written through the dynamic
  client with `unstructured`, converted to hand-written structs — so there is no
  code generation step and no generated clientset to keep in sync.
- **A CRD rather than a flag or an HTTP endpoint.** Isolation is desired state,
  not an event: `kubectl get netiso` shows what is isolated and why, RBAC is the
  API server's, and cleanup hangs off a finalizer.
- **`spec` is immutable.** Retargeting means delete, wait, recreate. It removes
  a whole code path, at the cost of an unprotected interval during replacement.
- **One active operation per namespace.** A second `NetworkIsolation` naming a
  namespace already in use is refused, because the first operation's policies
  are foreign to it.
- **Pods join and leave by label.** Membership is Kubernetes' evaluation of the
  selector, so scaling and rollouts are covered without the controller watching
  workloads or writing labels.
- **`lastReconcileTime` advances when the observation changes**, not on every
  pass, so an unchanged object produces no writes at all.

## Limitations

These are limits of NetworkPolicy, not of this implementation:

- Connections **already established** are not terminated. Only new ones are
  blocked.
- Protocols other than **TCP and UDP** are not covered.
- Traffic **relayed through a third workload** is not blocked. Only direct A↔B.
- Traffic whose **source address is translated** before it reaches the
  destination (NodePort, LoadBalancer, node-originated) is not covered.
- **hostNetwork pods** are refused at validation: upstream leaves their
  NetworkPolicy behaviour undefined.
- **Direct external ingress** to a selected pod is dropped; traffic arriving
  through an in-cluster proxy or ingress controller still works.
- **IPv4 kind with the default CNI only.** Other CNIs and IPv6 are untested.

## What I'd do with more time

<the G- rows from specs/001-network-isolation/spec.md, in that order: leader
election, naming the conflicting policy in the Degraded message, Degraded→Active
recovery tests, hostNetwork pods appearing between resyncs, richer status
conditions, kind verification in CI plus envtest, metrics and a dry-run preview>

## Time spent

<actual hours>
```

Fill every `<...>` placeholder with real content before committing — leaving one in is a failed deliverable, since NFR-06 requires the recorded versions and time.

- [ ] **Step 5: Run everything one last time**

```bash
make test cover
make verify-crd
make verify
```

Expected: unit tests PASS with coverage ≥90%, `AC-09 PASSED`, `AC-07 PASSED`.

- [ ] **Step 6: Commit**

```bash
git add hack/verify-isolation.sh README.md
git commit -m "feat(hack): live TCP/UDP verification script and README

Covers AC-07 and the NFR-06 reproducibility requirements."
```

---

## Acceptance criteria coverage

| ID    | Covered by                                                                      |
| ----- | ------------------------------------------------------------------------------- |
| AC-01 | Task 2, Step 1 — `TestBuildShape`                                               |
| AC-02 | Task 2, Steps 6–8 — `TestComplementTruthTable`, `TestComplementSharedNamespace` |
| AC-03 | Task 4, Step 1 — `TestValidateForeignPolicies`                                  |
| AC-04 | Task 6, Steps 1–4 — the five `TestCleanup*` tests                               |
| AC-05 | Task 4, Step 1 — `TestValidateRejections`                                       |
| AC-06 | Task 5, Step 7 — `TestReconcileIsIdempotent`                                    |
| AC-07 | Task 9 — `hack/verify-isolation.sh`                                             |
| AC-08 | Task 5, Step 9 — `TestPartialWriteDegradesAndConverges`                         |
| AC-09 | Task 3 — `hack/verify-crd.sh`                                                   |

## Deviations from the spec to confirm before merging

1. **`lastReconcileTime` semantics.** FR-05 calls it "the last reconcile time"; AC-06 requires an unchanged object to produce no further writes. Both cannot hold literally. This plan writes status only when something other than the timestamp changed, so the field records when the observation last changed. The README says so. If the spec means the timestamp should advance every pass, AC-06's assertion has to narrow to policy writes only — a one-line change in `TestReconcileIsIdempotent` and in `setStatus`.
2. **`Rejected` vs `Degraded` for the same precondition.** FR-05 defines
   `Rejected` as "a precondition failed; nothing was written", while the error
   table requires a foreign policy appearing _after_ activation to be
   `Degraded` with the owned policies retained. The plan resolves this by the
   finalizer: absent, nothing was ever written, so `Rejected`; present, the
   policies exist and stay, so `Degraded` and retried. Task 5, Step 11 pins both
   halves, and covers the `Degraded`→`Active` recovery the spec lists as a gap
   (G-03).
3. **The owner annotation.** `hardening.k8s.io/owner` is not in the spec. It exists so a NetworkPolicy event can be mapped back to its owning object without an in-memory index, which FR-04's "watch NetworkPolicy objects" otherwise requires. It is additive metadata on a policy this operation owns; it changes no behaviour the spec describes.
