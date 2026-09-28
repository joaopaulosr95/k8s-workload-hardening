# k8s-workload-hardening

An internal SRE tool for a Kubernetes cluster. **Core task 1 — on-demand network
isolation between two workloads — is implemented here.** Core task 2 (workload
hardening) is specified in `specs/002-workload-hardening/` but not built.

## What it does

Creating a `NetworkIsolation` object blocks direct network traffic between two
pod groups, each named by a namespace and a label selector. Deleting the object
restores the previous connectivity.

```yaml
apiVersion: hardening.acme.corp/v1alpha1
kind: NetworkIsolation
metadata:
  name: gateway-dashboard
  namespace: isolation-system
spec:
  peers:
    - namespace: tenant-a
      podSelector:
        matchLabels: { app: gateway }
    - namespace: tenant-b
      podSelector:
        matchLabels: { app: dashboard }
```

```console
$ kubectl apply -f deploy/samples/isolation.yaml
$ kubectl -n isolation-system get netiso
NAME                PHASE    PEER-0   PEER-1   MESSAGE   AGE
gateway-dashboard   Active   1        1                  22s
```

The two peers are **symmetric**. Traffic is blocked in both directions; their
order carries no meaning beyond indexing the two generated policies.

## Setup

```bash
make kind-up      # kind cluster, default CNI (enforces NetworkPolicy)
make deploy       # build image, load it, install CRD + RBAC + controller
make samples      # three namespaces with probe pods
make verify       # AC-07: live TCP/UDP traffic, before / after / removed
make verify-crd   # AC-09: API-server schema and immutability
make test         # unit tests, race detector
make cover        # coverage
```

## Tested versions

|                      |                                                                           |
| -------------------- | ------------------------------------------------------------------------- |
| kind                 | v0.32.0 (4-node cluster: 1 control-plane, 3 workers)                      |
| Kubernetes (kubelet) | v1.36.1                                                                   |
| kubectl              | v1.36.2                                                                   |
| Go                   | 1.27.1                                                                    |
| Docker               | 29.4.0                                                                    |
| CNI                  | kind's default (kindnetd), which enforces NetworkPolicy from kind v0.24.0 |

Behaviour on other CNIs is untested.

## How it works

NetworkPolicy is additive and allow-only: there is no deny rule, and a
restrictive policy cannot override another that permits the same traffic. A
prohibition is therefore written as _allow everything except the opposing
group_.

For a peer in namespace `N` with labels `k1=v1, k2=v2`, the policy protecting
the other peer allows:

1. every pod in every namespace other than `N`, via
   `kubernetes.io/metadata.name NotIn [N]`;
2. for each of that peer's label requirements, the pods in `N` failing it, via
   `k NotIn [v]` — which also matches a pod that **lacks the key entirely**.

The union of those branches is exactly "everything that is not that peer". Peer
count is `1 + len(matchLabels)`, so policy size is bounded by the schema.

That construction only holds while no other ingress policy in the namespace
re-allows what this one excludes. So the controller **refuses to operate in a
namespace that already contains a foreign ingress policy**, rather than
rewriting policies it does not own. One consequence: a second `NetworkIsolation`
naming either namespace is refused — one active operation per namespace, not per
pair.

## Decisions

- **Plain client-go, no framework.** The brief recommends it and the task needs
  nothing more. The custom resource is read and written through the _dynamic_
  client with `unstructured`, converted to hand-written structs via
  `runtime.DefaultUnstructuredConverter` — so there is no code-generation step
  and no generated clientset to keep in sync. Total dependency set is what
  `k8s.io/client-go` already pulls in.
- **A CRD rather than a flag or an HTTP endpoint.** Isolation is desired state,
  not an event: `kubectl get netiso` shows what is isolated and why, RBAC is the
  API server's, and cleanup hangs off a finalizer.
- **The API group is `hardening.acme.corp`, not `*.k8s.io`.** That suffix is
  reserved for APIs reviewed by the Kubernetes API committee; the API server
  refuses a CRD there without an `api-approved.kubernetes.io` annotation naming
  an accepted review. `acme.corp` stands in for the operating organisation's own
  domain.
- **`spec` is immutable**, enforced by a CEL transition rule. Retargeting means
  delete, wait for cleanup, recreate. It removes a whole code path, at the cost
  of an unprotected interval during replacement.
- **Exactly two peers per object.** One pair needs no policy compiler. Several
  pairs are separate objects, and the foreign-policy rule above refuses
  overlapping ones.
- **Pods join and leave by label.** Membership is Kubernetes' own evaluation of
  the selector, so scaling, rollouts and relabelling are covered without the
  controller watching workloads or writing labels. Anyone able to relabel a pod
  can move it out of a group; labels are trusted as the definition of membership.
- **Only `matchLabels`, 1–8 entries.** `matchExpressions` is rejected by the
  schema: the controller generates the negations itself, and accepting arbitrary
  expressions would mean inverting them correctly in every case.
- **Protected namespaces.** `kube-system`, `kube-public`, `kube-node-lease` and
  the controller's own namespace are never targeted; more can be added with
  `-protected-namespaces`.
- **`lastReconcileTime` advances when the observation changes**, not on every
  pass. Reconciling an unchanged object issues no writes at all — no policy
  update, no status update — so resyncs don't churn the API server or bury real
  changes in the audit log.
- **A failed write never rolls back.** If one of the two policies is written and
  the other fails, the successful one stays, the phase is `Degraded`, and the
  key is retried. Rolling back would reopen traffic the operator asked to block.

## Status

| Phase      | Meaning                                                   |
| ---------- | --------------------------------------------------------- |
| `Pending`  | Not yet evaluated                                         |
| `Rejected` | A precondition failed; nothing was written                |
| `Active`   | Both policies present and preconditions hold              |
| `Degraded` | Accepted, but incomplete or an assumption no longer holds |
| `Deleting` | Removal in progress                                       |

`Active` means the policies exist as configured. It does not assert that packets
have been verified — that is what `make verify` is for.

The same precondition failure reads differently before and after activation: the
finalizer is the marker. Absent, nothing was ever written, so `Rejected`.
Present, the policies are in place and stay there, so `Degraded` and retried —
and the object recovers by itself when the cause clears.

Contamination by a foreign ingress policy is tracked **per namespace**. Before
activation, a foreign policy in either namespace refuses the whole operation
(BR-03). After activation, only the affected namespace is left alone; the other
peer's policy is still repaired if something deletes it. Freezing both would
leave half the block missing while status claimed otherwise.

A transient API failure is reported too — `Pending` before activation,
`Degraded` after — rather than leaving a blank phase, which is
indistinguishable from a controller that is not running.

## Limitations

These are limits of NetworkPolicy, not of this implementation:

- Connections **already established** are not terminated. Only new ones are
  blocked.
- Protocols other than **TCP and UDP** are not covered.
- Traffic **relayed through a third workload** is not blocked. Only direct
  peer-to-peer.
- Traffic whose **source address is translated** before it reaches the
  destination (NodePort, LoadBalancer, node-originated) is not covered.
- **hostNetwork pods** are refused at validation: upstream leaves their
  NetworkPolicy behaviour undefined.
- **Direct external ingress** to a selected pod is dropped. An
  `ipBlock: 0.0.0.0/0` allowance would admit the whole non-pod address space —
  including any translated source — into the group being contained. Traffic
  arriving through an in-cluster proxy or ingress controller still works.
- **Node-originated traffic, including kubelet probes, matches no peer.** The
  policies allow pod sources only, so whether a probed workload keeps its
  readiness depends on how the CNI treats non-pod sources. On the tested CNI
  (kindnetd) probes are unaffected — the `gateway` sample carries a readiness
  probe and `make verify` asserts it stays Ready throughout. On a CNI that
  enforces ingress for node traffic, an isolated workload would drop out of its
  Service, which is worse than the block you asked for. Check this before using
  it anywhere else.
- **A foreign ingress policy is noticed on resync, not immediately.** The
  NetworkPolicy informer watches only policies this tool owns, so a foreign one
  appearing in a participating namespace is detected within one resync period
  (30s by default), not on arrival.
- **IPv4 kind with the default CNI only.** Other CNIs, IPv6 and dual-stack are
  untested.
- A namespace deleted and recreated under the same name is treated as the same
  target: namespace UIDs are not pinned.

## Tests

```console
$ make test
ok  github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1  coverage: 90.0%
ok  github.com/joaopaulosr95/k8s-workload-hardening/pkg/controller     coverage: 91.2%
ok  github.com/joaopaulosr95/k8s-workload-hardening/pkg/policy         coverage: 100.0%
```

Every acceptance criterion in `specs/001-network-isolation/spec.md` has a test.
Two are worth calling out, because they are the ones most easily got wrong:

**The complement truth table** (`pkg/policy/policy_test.go`). The whole feature
rests on `NotIn` also matching a pod that lacks the key. Get that wrong and the
policy either silently blocks unrelated pods or fails to block the target — and
both look fine in a YAML diff. The test evaluates the generated peers with the
same `metav1.LabelSelectorAsSelector` machinery the API server uses, against a
table that includes the missing-key row, the extra-labels row and the
same-labels-other-namespace row. Inverting `NotIn` to `In` fails it everywhere.

**The foreign-policy refusal** (`pkg/controller/validate_test.go`). NetworkPolicy
is additive, so an allow rule placed beside an existing ingress policy _widens_
access instead of narrowing it. The test covers the `policyTypes` defaulting rule
specifically: omitted, empty, explicit `Ingress`, and `[Egress, Ingress]` all
refuse; only an explicit `["Egress"]` is safe to ignore. That defaulting is the
part a reader is most likely to skip.

Fake-client tests are not evidence of packet enforcement. `make verify` is:
it asserts TCP **and** UDP, over pod IP **and** ClusterIP, in both directions,
before / after convergence / after deletion, while checking that DNS, an
unrelated pod, and the kubelet's readiness probe are unaffected throughout.

Nor are they evidence of RBAC. Switching the finalizer write from a PUT to a
patch passed every unit test and then failed in-cluster with a `Forbidden`,
because the ClusterRole granted `update` but not `patch`. Only `make deploy`
followed by `make verify` catches that class.

