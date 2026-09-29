---
name: On-demand network isolation
description: |
  Block network traffic between two pod groups, defined by namespace and label selector,
  through a NetworkIsolation custom resource. Reversible by deleting the resource.
author: João Bastos <joaopaulosr95@gmail.com>
status: Approved
---

# On-demand network isolation

## Objective

Let an SRE stop two workloads from exchanging network traffic on demand, and restore their previous
connectivity by removing the request. Traffic to and from everything else keeps working.

## Context

This covers core task 1 of the assignment. Workload hardening, core task 2, is not yet specified.

NetworkPolicies are additive and allow-only: there is no deny rule, and a restrictive policy cannot
override another policy that permits the same traffic. A prohibition must therefore be expressed as
_allow everything except the opposing group_, and it only holds if no other ingress policy in the
namespace re-allows what we exclude. The service refuses to operate in namespaces where that assumption
does not hold, rather than rewriting policies it does not own.

## Terminology

- **Group A / Group B:** the two pod sets being isolated, each defined by a namespace and a label
  selector.
- **Operation:** one NetworkIsolation object, identified by its UID.
- **Owned policy:** a NetworkPolicy labelled with this operation's UID.
- **Foreign ingress policy:** any other policy in a participating namespace that affects ingress,
  accounting for `policyTypes` defaulting.

## Business rules

### BR-01 — Scope of the block

Isolation prevents new TCP and UDP connections directly between A and B, in both directions, once
policy enforcement converges. Generated rules carry no port restrictions.

Not guaranteed: termination of connections already established, protocols other than TCP/UDP,
communication relayed through a third workload, and traffic whose source address has been translated
before it reaches the destination. These are limits of the mechanism, not of the implementation, and
must appear in the README.

### BR-02 — Environment

An IPv4 kind cluster using its default CNI, which enforces NetworkPolicy from kind v0.24.0 onward. The
README pins the tested versions. Behaviour on other CNIs is untested.

### BR-03 — Existing policies

Refuse to activate if either participating namespace already contains a foreign ingress policy, even one
selecting no pods. An additive allow rule placed alongside an existing ingress policy can widen access
rather than narrow it, and inferring compatibility is out of scope. Foreign egress-only policies —
those explicitly declaring `policyTypes: ["Egress"]` — are permitted and left untouched.

This also means a second NetworkIsolation naming either namespace is refused, because the first
operation's policies are foreign to it — one active operation per namespace, not per pair.

Never delete or modify a policy this operation does not own.

### BR-04 — Selection

Each group declares a namespace and a `podSelector` holding a non-empty `matchLabels` map. Membership
is evaluated by Kubernetes from current pod labels, so pods join and leave as they are created,
relabelled, scaled or replaced. The controller never reads workload objects and never writes labels.

Selectors matching zero pods are valid: the policies are installed and the status reports zero. If both
groups share a namespace, their selectors must be provably disjoint — a shared key with different
values. Reject otherwise, including when both groups are currently empty.

Reject currently matching `hostNetwork` pods: NetworkPolicy behaviour for them is undefined upstream.

### BR-05 — Protected namespaces

Never target `kube-system`, `kube-public`, `kube-node-lease`, or the controller's own namespace.
Additional protected namespaces are configurable. Checked before anything is written.

### BR-06 — Restoration

Deleting the NetworkIsolation removes the policies it owns. If nothing else in the cluster changed, the
previous connectivity returns once enforcement converges. Nothing else is restored — the service keeps
no snapshot of foreign resources.

## Functional requirements

### FR-01 — Interface

A namespaced CRD: group `hardening.acme.corp`, version `v1alpha1`, kind `NetworkIsolation`, with a
structural schema and the status subresource. Both groups declare their namespace explicitly, so the
object can live in a namespace of the operator's choosing — including one neither group occupies.

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

Both groups are required. `matchLabels` holds 1–8 entries; `matchExpressions` and empty selectors are
not accepted from operators. The schema validates shape, namespace names and label syntax; namespace
existence, hostNetwork membership and policy conflicts are checked by the controller and reported in
status.

`spec` is immutable, enforced by a CEL transition rule. To retarget, delete the object, wait for
cleanup, and create a new one. This removes the retargeting code path entirely.

Creating the object requests isolation; deleting it requests removal. There is no enable flag.

### FR-02 — Generated policies

Two ingress-only NetworkPolicies, one per group, each selecting its own group and allowing every pod
source except the opposing group. For group B in namespace `N` with labels `k1=v1, k2=v2`, the policy
protecting A allows:

1. all pods in namespaces other than `N`, via `kubernetes.io/metadata.name NotIn [N]`;
2. for each of B's label requirements, pods in `N` failing that requirement, via `k NotIn [v]`.

A pod satisfies (2) when it carries a different value **or lacks the key entirely** — `NotIn` matches
absent keys. The union of those branches is exactly "everything in `N` that is not B". Peer count is
`1 + len(B.matchLabels)`, so policy size is bounded by the schema without further limits.

No egress rules. No `ipBlock`. No enumeration of pod IPs. Because pods are allowed as sources, traffic
arriving through an in-cluster proxy or ingress controller continues to work; direct external ingress to
a selected pod does not.

### FR-03 — Ownership and cleanup

Policy names are derived deterministically from the operation UID, and each policy carries that UID as a
label. Only policies bearing it are ever updated or deleted; a name collision with a foreign object is
reported, never overwritten.

Before writing any policy, persist the finalizer `hardening.acme.corp/cleanup`. On deletion: remove both
owned policies, tolerating ones already absent, then remove the finalizer. Cleanup must not depend on
matching pods, namespaces or preconditions still being valid. Cross-namespace owner references do not
work, so cleanup is explicit.

A restart resumes deletion rather than recreating policies. Desired state is re-derived from `spec` on
every reconcile, so a crash between writes is repaired by the next pass.

### FR-04 — Reconciliation

One controller replica. Kubernetes does not guarantee that, so single-writer exclusivity is an
operational precondition rather than a property this service enforces; deterministic policy names and
idempotent writes keep concurrent reconciliation of one object safe. Watch NetworkIsolation and
NetworkPolicy objects, with a periodic resync that re-validates preconditions. API calls carry timeouts.

For each object: check `deletionTimestamp` first — deletion always takes precedence over activation;
validate preconditions; persist the finalizer; write both policies; update status. Retry incomplete work
on the next pass. If one policy write fails, keep the one that succeeded, report `Degraded`, and retry —
never roll back to reopen traffic automatically.

### FR-05 — Status

| Phase      | Meaning                                                   |
| ---------- | --------------------------------------------------------- |
| `Pending`  | Not yet evaluated                                         |
| `Rejected` | A precondition failed; nothing was written                |
| `Active`   | Both policies present and preconditions hold              |
| `Degraded` | Accepted, but incomplete or an assumption no longer holds |
| `Deleting` | Removal in progress                                       |

Status carries the phase, a message naming the specific cause, the owned policy names, the pod count
matched in each group, and the last reconcile time. `Active` means the policies exist as configured; it
does not assert that packets have been verified. Counts are observations, and zero is reported
explicitly rather than hidden.

## Non-functional requirements

- **NFR-01 — Simplicity.** Idiomatic Go with client-go. Policy generation is a pure function, separately
  testable from reconciliation.
- **NFR-02 — Recovery.** Crashes, API errors and repeated requests neither lose the request nor mutate
  resources this operation does not own.
- **NFR-03 — Access.** A ClusterRole: `networkpolicies` create/get/list/watch/update/delete cluster-wide
  (target namespaces are chosen at runtime, so this cannot be namespace-scoped); `pods` and `namespaces`
  get/list/watch; `networkisolations` plus its status and finalizer. No workload reads, no pod exec, no
  secrets.
- **NFR-04 — Visibility.** Log the operation UID, the decision taken, and any failure, identifying the
  resource and whether work remains.
- **NFR-05 — Verification.** Every acceptance criterion has an automated test; each code unit reaches
  90% unit coverage per `AGENTS.md`. Fake-client tests are not evidence of packet enforcement.
- **NFR-06 — Reproducibility.** CRD, controller and RBAC manifests, sample workloads, and a runnable
  kind script. Record tested versions in the README.

## Acceptance criteria

| ID    | Scenario                                                                                                                                                                                             | Evidence |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| AC-01 | Two group selectors generate exactly two ingress-only policies with the expected complement peers, no egress and no ipBlock                                                                          | Unit     |
| AC-02 | Complement truth table: a pod with a different label value, or missing the key, is allowed; one matching every label is excluded. Covers both groups in the same namespace                           | Unit     |
| AC-03 | A foreign ingress policy in either namespace prevents activation and writes nothing. An explicit `policyTypes: ["Egress"]` policy does not, and is left unchanged                                    | Unit     |
| AC-04 | Deletion removes both policies then the finalizer; absent policies are tolerated; an API error never implies absence; a restart mid-deletion resumes rather than recreating                          | Unit     |
| AC-05 | Missing namespace, protected namespace, overlapping selectors in a shared namespace, and a matching hostNetwork pod each yield `Rejected` with a distinct reason and no writes                       | Unit     |
| AC-06 | Reconciling an unchanged object repeatedly produces no further writes; zero-match selectors stay `Active` with counts of zero                                                                        | Unit     |
| AC-07 | On kind: TCP **and** UDP traffic A↔B, over pod IP and ClusterIP, succeeds before, fails after convergence, and returns after deletion. DNS and traffic to an unrelated pod are unaffected throughout | Script   |
| AC-08 | A partial write — one policy created, the other failing — yields `Degraded`, keeps the created policy, and converges on retry without reopening traffic                                              | Unit     |
| AC-09 | The CRD installs and the API server rejects an object missing a group, supplying `matchExpressions`, or edited after creation                                                                        | Script   |

AC-02 and AC-03 are the cases most easily got wrong. AC-02 catches an inverted or incomplete negation,
which would silently block unrelated pods or fail to block the target — the `NotIn`-matches-absent-key
rule is the part that is easy to reason about incorrectly. AC-03 catches the additive-policy trap, where
an allow rule added beside an existing ingress policy widens access instead of narrowing it.

## Error cases

| Case                                            | Behaviour                                                                          |
| ----------------------------------------------- | ---------------------------------------------------------------------------------- |
| Invalid shape or an edit to an immutable spec   | Rejected by the API server; the stored object and its policies are untouched       |
| Precondition fails before activation            | `Rejected`, with the cause in the message; nothing written                         |
| Foreign ingress policy appears after activation | `Degraded`, naming the policy; owned policies retained; resumes when it disappears |
| One of the two policy writes fails              | `Degraded`; the successful write is kept; retried                                  |
| A participating namespace disappears            | `Degraded`; deletion of the object still works                                     |
| Policy name occupied by a foreign object        | Conflict reported; never overwritten                                               |
| Controller unavailable                          | Deletion stays pending behind the finalizer and resumes on restart                 |
| API error during cleanup                        | Finalizer retained; retried; never assumed to mean the policy is gone              |

## Out of scope

What this feature does not do, split by whether the exclusion is a choice (`D-`, declined) or a gap
(`G-`, not yet built).

### Declined

Considered and deliberately not built. More time would not change these.

| #    | Excluded                                                                      | Why                                                                                                                                                                                                   |
| ---- | ----------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D-01 | More than two groups per one object                                           | One pair needs no policy compiler — peer count is `1 + len(labels)`. Several pairs are separate objects, and BR-03 refuses overlapping ones.                                                          |
| D-02 | Preserving direct external ingress to a selected pod                          | An `ipBlock: 0.0.0.0/0` allowance would admit the whole non-pod address space, including any translated source, into the group being contained. Ingress routed through an in-cluster pod still works. |
| D-03 | Namespace reservations between operations                                     | BR-03 already refuses a second operation once the first has written its policies.                                                                                                                     |
| D-04 | A durable acceptance checkpoint                                               | `spec` is the desired state and each reconcile re-derives it. Consequence: namespace UIDs are not pinned, so a namespace deleted and recreated under the same name is treated as the same target.     |
| D-05 | In-place retargeting                                                          | `spec` is immutable: delete, wait for cleanup, recreate. Consequence: an unprotected interval during replacement.                                                                                     |
| D-06 | Rewriting, adopting, merging or restoring policies owned by anyone else       | Refusing is honest; reconciling independently authored allow rules is a policy engine.                                                                                                                |
| D-07 | Terminating connections already established                                   | Upstream leaves this implementation-defined.                                                                                                                                                          |
| D-08 | Blocking traffic relayed through a third workload                             | Only direct A↔B is addressed.                                                                                                                                                                         |
| D-09 | `hostNetwork` pods                                                            | NetworkPolicy behaviour for them is undefined upstream; currently matching pods are refused at validation.                                                                                            |
| D-10 | Defending against pod label changes                                           | Labels are trusted as the definition of membership. Anyone able to relabel a pod can move it out of a group.                                                                                          |
| D-11 | IPv6 and dual-stack                                                           | Untested.                                                                                                                                                                                             |
| D-12 | `matchExpressions`, empty selectors and namespace selectors in operator input | Equality maps only; the controller generates the negations itself.                                                                                                                                    |
| D-13 | Named workload or Pod references, and workload-template inspection            | Selection depends only on namespace and current pod labels.                                                                                                                                           |
| D-14 | NAT, NodePort, LoadBalancer and node-originated traffic                       | Only ordinary pod and ClusterIP paths are supported.                                                                                                                                                  |
| D-15 | Helm packaging, multiple served CRD versions, and operation history           | Not required by the brief.                                                                                                                                                                            |

### Deferred

Real gaps, not present in this version. Ordered by what would be addressed first.

| #    | Not yet done                                                                                                        | Interim position                                                                                                                           |
| ---- | ------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| G-01 | Leader election, so more than one controller replica is safe                                                        | Single-writer exclusivity is stated as an operational precondition in FR-04. The exposure is two objects accepted inside one short window. |
| G-02 | Naming the conflicting policy and the affected group in the `Degraded` message                                      | A conflict is reported, but not the fact that retained policies may still be admitting traffic the foreign policy meant to block.          |
| G-03 | Tests for the transition out of `Degraded` back to `Active` after a foreign policy or missing namespace is restored | AC-08 covers recovery from a partial write; recovery from an environmental cause is untested.                                              |
| G-04 | Detecting a `hostNetwork` pod that appears between resyncs                                                          | Checked at acceptance and on resync only.                                                                                                  |
| G-05 | Conditions, per-policy outcomes and `observedGeneration` in status                                                  | Phase and message are enough to operate the tool.                                                                                          |
| G-06 | Running the kind verification in CI, plus envtest coverage for CRD validation and finalizer semantics               | One runnable script covers AC-07; fake clients cannot exercise either of those.                                                            |
| G-07 | A metrics endpoint, and a dry-run preview                                                                           | Both listed as bonuses; a preview is required for workload hardening and should be shared if built there.                                  |

## References

- [NetworkPolicy concepts](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
- [NetworkPolicy API](https://kubernetes.io/docs/reference/kubernetes-api/policy-resources/network-policy-v1/)
- [Label selector semantics](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/)
- [CRDs, validation and status subresources](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/)
- [Finalizers](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)
- [Cross-namespace ownership](https://kubernetes.io/docs/concepts/architecture/garbage-collection/)
