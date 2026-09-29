# Reference

Look-up only. For why any of this is the way it is, read the specs:
[001](../specs/001-network-isolation/spec.md), [002](../specs/002-workload-hardening/spec.md),
[003](../specs/003-bonus/spec.md). For the fields of any custom resource, `kubectl explain`
prints the schema this repository ships — it is not duplicated here.

```console
$ kubectl explain workloadhardening.spec
$ kubectl explain workloadhardening.spec.approvedPlan
$ kubectl explain networkisolation.spec.peers
```

## Custom resources

| Kind | Plural | Short | Scope |
| --- | --- | --- | --- |
| `NetworkIsolation` | `networkisolations` | `netiso` | Namespaced |
| `WorkloadHardening` | `workloadhardenings` | `wh` | Namespaced |

Both are `hardening.acme.corp/v1alpha1`, and both have a status subresource.

## Annotations and labels

| Key | Written on | Written by | Removed by |
| --- | --- | --- | --- |
| `hardening.acme.corp/filled` | the patched workload | `WorkloadHardening` | nothing — it outlives the object that wrote it, deliberately |
| `hardening.acme.corp/skip` | a workload | a human | whoever wrote it, and nobody else |
| `hardening.acme.corp/owner` | a generated NetworkPolicy | `NetworkIsolation` | its finalizer |
| `hardening.acme.corp/operation` (label) | a generated NetworkPolicy | `NetworkIsolation` | its finalizer |

`filled` records the leaf path of every field written and the value written to it. It is the
checkpoint a manual revert works from, and nothing in this tool removes it.

`skip` is a human's exemption. Nothing in this tool ever writes or removes it.

## Finalizers

| Finalizer | On | Removes |
| --- | --- | --- |
| `hardening.acme.corp/cleanup` | `NetworkIsolation` | the NetworkPolicies it generated |

`WorkloadHardening` carries no finalizer: deleting it leaves the patches and the provenance
annotation in place.

## Phases

| Phase | Kinds | Terminal |
| --- | --- | --- |
| `Pending` | both | no |
| `Rejected` | both | no — re-evaluated every resync |
| `Active` | `NetworkIsolation` | no |
| `Degraded` | `NetworkIsolation` | no |
| `Deleting` | `NetworkIsolation` | no |
| `Previewed` | `WorkloadHardening` | no |
| `Applied` | `WorkloadHardening` | yes, until `spec.approvedPlan` changes |
| `PartiallyApplied` | `WorkloadHardening` | no |

## Per-target outcomes

| Outcome | Meaning |
| --- | --- |
| `Planned` | would change, on an object nobody has armed |
| `Patched` | fields written |
| `Failed` | the API server refused it |
| `Stale` | the approved hash no longer describes this target |
| `Unapproved` | not in `spec.approvedPlan` |

## Controller flags

| Flag | Default | Effect |
| --- | --- | --- |
| `-kubeconfig` | `""` (in-cluster) | path to a kubeconfig |
| `-resync` | `30s` | how often every object re-validates its preconditions |
| `-timeout` | `30s` | deadline for one reconcile pass's API calls |
| `-protected-namespaces` | `""` | comma-separated, in addition to the built-ins |
| `-metrics-addr` | `:8080` | metrics listener; empty disables it |

Built-in protected namespaces: `kube-system`, `kube-public`, `kube-node-lease`, and the
controller's own namespace, read from `POD_NAMESPACE`.

## Metrics

Served on `-metrics-addr` at `/metrics`, from a registry private to this process.

| Series | Type | Labels |
| --- | --- | --- |
| `hardening_reconcile_total` | counter | `resource`, `phase` |
| `hardening_targets_patched_total` | counter | — |
| `hardening_dryrun_refusals_total` | counter | — |
| `hardening_apply_failures_total` | counter | — |
| `hardening_queue_depth` | gauge | — |

Both labels are closed sets. Neither a namespace nor an object name ever becomes a label value.

`hardening_reconcile_total` is a `CounterVec` and exposes nothing at all — not even a `# TYPE`
line — until its first observation.

## RBAC

One ClusterRole, cluster-scoped because target namespaces are chosen at runtime.

| Resource | Verbs |
| --- | --- |
| `networkpolicies` | create, get, list, watch, update, delete |
| `pods`, `namespaces` | get, list, watch |
| `networkisolations` | get, list, watch, update, patch |
| `networkisolations/status` | get, update, patch |
| `networkisolations/finalizers` | update |
| `deployments`, `statefulsets`, `daemonsets` | get, list, patch |
| `replicasets` | get, list |
| `jobs`, `cronjobs` | get, list |
| `limitranges` | get, list |
| `workloadhardenings` | get, list, watch |
| `workloadhardenings/status` | get, update, patch |

Serving metrics needs no rule: it reads nothing from the API.

## Deployed objects

| Object | Name | Namespace |
| --- | --- | --- |
| Deployment | `k8s-workload-hardening` | `isolation-system` |
| ServiceAccount | `k8s-workload-hardening` | `isolation-system` |
| ClusterRole, ClusterRoleBinding | `k8s-workload-hardening` | — |
| Service | `k8s-workload-hardening-metrics` | `isolation-system` |
