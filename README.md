# k8s-workload-hardening

Two Kubernetes controllers in one binary. `NetworkIsolation` writes the pair of NetworkPolicies
that isolate two groups of pods from each other. `WorkloadHardening` fills in missing resource
requests and missing securityContext fields across namespaces, previewing every change and
taking approval per workload before it writes anything.

- **New here?** [Tutorial](docs/tutorial.md) — one path, cluster to hardened workload, ~15 minutes.
- **Trying to do something?** [How-to](docs/how-to.md) — four goals, commands first.
- **Looking something up?** [Reference](docs/reference.md) — annotations, phases, flags, RBAC, metrics.
- **Want to know why?** [specs/](specs/) — every decision, with the alternative named and refused.

## Requirements

|                      |                                                                           |
| -------------------- | ------------------------------------------------------------------------- |
| kind                 | v0.32.0 (4-node cluster: 1 control-plane, 3 workers)                      |
| Kubernetes (kubelet) | v1.36.1                                                                   |
| kubectl              | v1.36.2                                                                   |
| Go                   | 1.27.1                                                                    |
| Docker               | 29.4.0                                                                    |
| CNI                  | kind's default (kindnetd), which enforces NetworkPolicy from kind v0.24.0 |

Behaviour on other CNIs is untested.

```console
$ make kind-up && make deploy && make verify
```
