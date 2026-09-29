# k8s-workload-hardening

An internal SRE tool for a Kubernetes cluster. Both core tasks are implemented
in one binary: **core task 1 — on-demand network isolation between two
workloads**, and **core task 2 — on-demand workload hardening**, which fills in
missing resource requests and missing `securityContext` hardening after
previewing every change and having each target approved by its own hash.
Their specs are `specs/001-network-isolation/spec.md` and
`specs/002-workload-hardening/spec.md`.

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
make samples-isolation      # three namespaces with probe pods
make verify-isolation       # AC-07: live TCP/UDP traffic, before / after / removed
make verify-crd-isolation   # AC-09: API-server schema and immutability
make test                   # unit tests, race detector
make cover                  # coverage, and the 90% floor AGENTS.md asks for

# core task 2
make samples-hardening      # harden-a and harden-b: five Deployments, one LimitRange
make verify-hardening       # AC-15: preview, approve, apply, rollout, QoS, provenance
make verify-crd-hardening   # AC-16: API-server schema and per-field immutability

make verify                 # both features
make verify-crd             # both CRD suites
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
ok  github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1  coverage: 91.7%
ok  github.com/joaopaulosr95/k8s-workload-hardening/pkg/controller     coverage: 90.2%
ok  github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan           coverage: 98.0%
ok  github.com/joaopaulosr95/k8s-workload-hardening/pkg/policy         coverage: 100.0%
```

Every acceptance criterion in both specs has a test.
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

## Core task 2 — on-demand workload hardening

Creating a `WorkloadHardening` object **previews** what would change across the
namespaces it names. Nothing is written until the operator copies the hashes
they accept into `spec.approvedPlan`.

    kubectl apply -f deploy/samples/hardening.yaml
    kubectl -n isolation-system get wh tenant-hardening -o jsonpath='{.status.plan[*].hash}'
    kubectl -n isolation-system patch wh tenant-hardening --type=merge \
      -p '{"spec":{"approvedPlan":["<hash>","<hash>"]}}'

    make samples-hardening      # harden-a and harden-b, five Deployments, one LimitRange
    make verify-hardening       # AC-15: preview, approve, apply, rollout, QoS, provenance
    make verify-crd-hardening   # AC-16: API-server schema and per-field immutability

### What it fills

| Field                      | Level     | Value            | Written                  |
| -------------------------- | --------- | ---------------- | ------------------------ |
| `runAsNonRoot`             | pod       | `true`           | unless root is evidenced |
| `seccompProfile.type`      | pod       | `RuntimeDefault` | always                   |
| `allowPrivilegeEscalation` | container | `false`          | always                   |
| `capabilities.drop`        | container | `["ALL"]`        | always                   |
| `readOnlyRootFilesystem`   | container | `true`           | only when requested      |

plus absent **resource requests**, from the values in the object. Limits are
never written.

### Decisions

- **Gaps are judged on the effective value, not on what the template says.**
  Kubernetes copies `limits` into `requests` when requests are absent, and it
  does so when defaulting the **Pod** — never the workload template. A
  Deployment declaring `limits: {cpu: 500m, memory: 1Gi}` and no requests runs
  as QoS `Guaranteed` with `requests == limits`. A tool that reads templates
  sees an absent field and calls it a gap; filling it would cut the CPU
  reservation 50×, the memory reservation 32×, and demote the pod to
  `Burstable`. So a `limits`-only container is a **finding**, never a gap — and
  the same rule applies to a namespace LimitRange, whose `default` supplies the
  request when `defaultRequest` is omitted.
- **`runAsNonRoot` is written at pod level, so one container needing root
  suppresses it for the whole pod.** Evidence is an explicit `privileged: true`
  or an effective `runAsUser: 0`. Reporting such a container as a finding while
  still writing the field is the most likely way a tool like this takes out a
  DaemonSet: CNI agents, log shippers and node exporters are routinely
  privileged without ever declaring `runAsUser: 0`, and no dry-run refuses it —
  the failure is the kubelet's.
- **Approval is per target, by hash.** A plan-wide hash cannot converge: any CI
  deploy touching any workload in any of the named namespaces moves it. Per
  target also lets an operator approve a subset deliberately, which is the
  normal way to use a gate like this.
- **No finalizer, no workload informer, no drift repair.** The tool does not own
  the fields it writes. A reconcile loop would eventually overwrite a deliberate
  later change — someone raising a memory limit after an OOMKill — and restart
  pods to do it. Deleting the request leaves the patches in place, so no object
  is ever stuck waiting on this controller.
- **Provenance lives on the target, not only in status.** Status dies with the
  custom resource, and an operator inspecting a workload should be able to see
  what changed it without knowing this tool exists.
- **Limits are never written.** The tool cannot know a workload's working set,
  and one number spread across sixteen namespaces is guaranteed wrong for some
  of them. A memory limit that is too small kills the container after a rollout
  that completed green. LimitRange is the per-namespace mechanism that exists
  for this, and the tool routes operators to it.

### Limitations

- **The dry-run is not a safety net for securityContext.** It catches schema,
  admission and webhook problems. Every root-related failure is enforced by the
  kubelet or the kernel and passes a dry-run cleanly.
- **`Applied` means the API server accepted every approved patch.** It does not
  assert that the resulting pods became Ready. A halted rollout is visible in
  the workload but not in this object's status.
- **A patched pod that becomes Ready and fails later is not detected** — a
  denied syscall, a missing capability, a setuid exec. Those three are the
  restricted PSS baseline and are the feature. `readOnlyRootFilesystem` is
  opt-in precisely because its failure is reliably late.
- **The tool cannot harden a workload that is unhardened by explicit choice.**
  It reports it instead; it never overrules a decision someone made on purpose.
- **A template patch restarts every pod of every target.** The preview reports
  the pod count and the rollout mechanism per target; the tool does not stage,
  throttle or canary.
- **There is no undo in this version.** The provenance annotation is the
  checkpoint one would work from.
- **Two requests may name the same namespace**, and the second one's provenance
  annotation replaces the first's.
- **`create` on `workloadhardenings` is effectively a cluster-wide
  workload-mutation grant.** The object names its target namespaces in `spec`
  and may live in a namespace of the operator's choosing, so anyone who can
  create one anywhere can direct the controller to patch workloads in any
  non-protected namespace. That is inherent to the interface rather than a
  defect — protected namespaces and the per-target approval gate are what bound
  it — but the verb belongs with cluster admins, not with namespace owners.

