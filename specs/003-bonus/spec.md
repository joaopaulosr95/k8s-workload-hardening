# Bonus

## Refactor

Act as an independent reviewer and take the following topics as a starting
point. Feel free to recommend other fixes/changes once you're done with those.

- Standardize naming - network isolation was built under the assumption nothing
  else would exist in this project, so we have types like V1Alpha.Resource and
  V1Alpha.HardeningResource, Reconciler and HardeningReconciler, or even files
  like types.go and hardening.go
- Restructure docs - currently the README was built after core task 001 and
  then core task 002 was just appended to the end of the document
- Review architecture - lookup for potential code optimizations, duplicated
  and/or unclear snippets, cyclomatic complexity, adherence to custom
  Kubernetes controllers best practices

## Extra features

### Undo for workload hardening

Cheaper than it looks, because of BR-01 in `specs/002-workload-hardening/spec.md`: the tool only
ever fills a field whose **effective value** is absent, and never overwrites one. The inverse of
every change is therefore "delete this field" — there is no previous value to snapshot.

The checkpoint is the **provenance annotation** `hardening.acme.corp/filled` on the patched
workload (BR-08), not `status.applied[]`. Status dies with the custom resource: one-shot
semantics and no finalizer mean deleting the object destroys any record living there. The
annotation outlives it, which is what makes an undo a standalone operation — it needs a list of
namespaces, not the original object.

- An undo request names namespaces and reverts every workload carrying the annotation. Same pure
  function, same server-side dry-run, same per-target hash approval as an apply (BR-07), so an
  undo is previewable on machinery that already exists.
- Per recorded **leaf** path, a strategic merge patch setting it to `null`. Leaf paths are why
  this is safe: deleting `resources` wholesale would take a `limits` block a human added after
  the apply.
- **Delete a field only if its current value still equals the recorded one.** The annotation
  records values, not just paths, precisely so that a human's later edit is left alone — BR-01's
  "never overrule a human" rule pointed the other way.
- Removing a field is not always allowed. In a namespace with
  `pod-security.kubernetes.io/enforce: restricted`, deleting `seccompProfile` or `runAsNonRoot`
  makes the template non-compliant; `enforce` applies to **pods**, so the workload patch succeeds
  and then every new pod is rejected while the rollout halts. An undo has to check the namespace's
  PSS labels first, and the dry-run will not do it for you.
- Costs a second rollout — removing requests restarts the pods again, with the same per-kind
  blast radius as BR-02's table.
- Nothing to undo if the workload was replaced: delete and recreate it and the annotation goes
  too, correctly, because the new template was never patched.
- **Open:** a workload patched by two objects in sequence. The second annotation write replaces
  the first, so object 1's record is lost — recorded as G-03 in 002. 001 refuses this per
  namespace (BR-03 there); 002 does not.

### Metrics endpoint + Grafana stack

### Integration tests using Kind

#### NetworkIsolation

- Create 2 namespaces tenant-a and tenant-b
- Deploy two alpine containers
- Run netcat/ping against each other
- Deploy NetworkIsolation
- Check netcat/ping
- Remove NetworkIsolation
- Check if traffic is back

#### Workload hardening

##### Resources

- Deploy alpine pods
  - No limits or requests
  - Only requests
  - Only limits
- Deploy LimitQuotas and check how they conflict
- Undo

##### SecurityContext

- Non-root user
- What makes a SecurityContext hardened?
- Undo
