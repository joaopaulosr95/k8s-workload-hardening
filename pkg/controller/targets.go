package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/plan"
)

// HardeningReconciler drives WorkloadHardening objects. It shares the binary,
// the clients and the workqueue with the NetworkIsolation reconciler but keeps
// its own state: the dry-run cache of FR-03.
//
// There is no finalizer field and no workload informer, deliberately. Isolation
// is desired state and must persist while pods come and go; hardening is not,
// and a field written into a workload's own template needs no custodian. A
// continuous reconcile would eventually overwrite a deliberate later change —
// someone raising a memory limit after an OOMKill — and start a rollout to do
// it (FR-05, D-02).
type HardeningReconciler struct {
	Kube      kubernetes.Interface
	Dyn       dynamic.Interface
	Protected map[string]bool
	Timeout   time.Duration
	Now       func() time.Time

	// mu guards verified, which one worker writes and which Run's resync
	// reads. The queue serialises a key, but the map is shared across keys.
	mu sync.Mutex
	// verified records, per request and target, the change hash whose dry-run
	// the API server last accepted. Consulted only while unarmed (FR-03).
	verified map[string]string
}

// Rollout mechanisms, per BR-02's table. Reported so that a single-replica
// StatefulSet is visibly a different proposition from a three-replica
// Deployment (BR-09). The tool does not stage, throttle or canary: the
// restarts are inherent to what was asked for, and the workload's own
// maxUnavailable and readiness gating are the mechanism that bounds them
// (D-10).
// BR-02's table is captioned "defaults; an operator can override", so the
// defaults are what these describe when the workload configures nothing. Where
// it does configure something the reported mechanism is read off the object:
// the whole point of the field is that the operator accepting the restart can
// see what it costs for *this* workload, and a Deployment set to Recreate
// takes every pod down at once — the inverse of the surge text below.
// Each is a short mechanism plus the numbers that bound it. The prose spelling
// out what each one costs lives in the CRD's field description and the README:
// status carries one of these per target, and a namespace of several hundred
// workloads should not spend its object size limit on a repeated paragraph.
const (
	rolloutDeployment  = "RollingUpdate: maxSurge 25%, maxUnavailable 25% (defaults)"
	rolloutRecreate    = "Recreate: all pods down, then replaced"
	rolloutStatefulSet = "RollingUpdate: reverse ordinal, one at a time, no surge"
	rolloutDaemonSet   = "RollingUpdate: maxUnavailable 1, maxSurge 0 (defaults), per node"
)

// deploymentRollout describes how this Deployment in particular rolls out.
func deploymentRollout(d *appsv1.Deployment) string {
	if d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		return rolloutRecreate
	}
	ru := d.Spec.Strategy.RollingUpdate
	if ru == nil || (ru.MaxSurge == nil && ru.MaxUnavailable == nil) {
		return rolloutDeployment
	}
	return fmt.Sprintf("RollingUpdate: maxSurge %s, maxUnavailable %s",
		orDefault(ru.MaxSurge, "25%"), orDefault(ru.MaxUnavailable, "25%"))
}

// daemonSetRollout does the same for a DaemonSet. maxSurge is reported because
// a non-zero one changes the mechanism from delete-then-create to a surge.
func daemonSetRollout(d *appsv1.DaemonSet) string {
	ru := d.Spec.UpdateStrategy.RollingUpdate
	if ru == nil || (ru.MaxSurge == nil && ru.MaxUnavailable == nil) {
		return rolloutDaemonSet
	}
	return fmt.Sprintf("RollingUpdate: maxUnavailable %s, maxSurge %s, per node",
		orDefault(ru.MaxUnavailable, "1"), orDefault(ru.MaxSurge, "0"))
}

// statefulSetRollout does the same for a StatefulSet. It has no surge and no
// maxUnavailable worth reporting here: a partition above 0 is refused by
// BR-04, and maxUnavailable only widens the one-at-a-time default.
func statefulSetRollout(*appsv1.StatefulSet) string { return rolloutStatefulSet }

// orDefault renders an optional IntOrString, naming the documented default
// where the workload leaves it unset.
func orDefault(v *intstr.IntOrString, fallback string) string {
	if v == nil {
		return fallback
	}
	return v.String()
}

// fromCache asks the API server to serve a list from its watch cache rather
// than from a quorum read. It is used only for the lists that exist to produce
// findings — pods, jobs, cronjobs, replicasets — where a slightly stale answer
// is still a true observation and the next resync corrects it. The workload
// lists that decide what gets patched keep their strong reads: a patch is
// written against what the list returned, and that must not be stale.
//
// It matters because discovery runs per namespace on every resync of every
// non-terminal object, and the pod list in particular walks every pod in the
// namespace purely to report the bare ones.
var fromCache = metav1.ListOptions{ResourceVersion: "0"}

// Refusal reasons. Each is distinct, because "refused" without the cause tells
// an operator nothing about which knob to turn (AC-07).
const (
	refusedPaused   = "spec.paused is true: the patch would not roll out, so it would sit inert and the workload would break at a later drain, eviction or scale (BR-04)"
	refusedOnDelete = "the update strategy is OnDelete: the patch would not roll out, so it would sit inert and the workload would break at a later drain, eviction or scale (BR-04)"
)

// hardeningTarget is one workload this pass will consider.
type hardeningTarget struct {
	Ref plan.Target
	// Pod is the target's spec.template.spec. It is the only thing read and
	// the only thing patched; running pods are never patched (Terminology).
	Pod *corev1.PodSpec
	// Pods is how many pods a patch would restart (BR-09).
	Pods int
	// Rollout is the mechanism from BR-02's table that applies to this kind.
	Rollout string
	// patch issues one strategic merge patch against this target's own kind,
	// so nothing downstream needs a type switch.
	patch func(ctx context.Context, body []byte, opts metav1.PatchOptions) error
}

// discover enumerates one namespace: every target this tool may patch, in a
// deterministic order, and a finding for everything it may not.
func (r *HardeningReconciler) discover(ctx context.Context, namespace string) ([]hardeningTarget, []v1alpha1.Finding, error) {
	var (
		targets  []hardeningTarget
		findings []v1alpha1.Finding
	)
	report := func(kind, name, reason string) {
		findings = append(findings, v1alpha1.Finding{Namespace: namespace, Kind: kind, Name: name, Reason: reason})
	}

	apps := r.Kube.AppsV1()

	deployments, err := apps.Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if why := excluded(d); why != "" {
			report("Deployment", d.Name, why)
			continue
		}
		// A Deployment has no OnDelete strategy and no partition; paused is
		// the only configuration that stops the patch rolling out.
		if d.Spec.Paused {
			report("Deployment", d.Name, refusedPaused)
			continue
		}
		name := d.Name
		targets = append(targets, hardeningTarget{
			Ref:     plan.Target{Namespace: namespace, Kind: "Deployment", Name: name},
			Pod:     &d.Spec.Template.Spec,
			Pods:    int(d.Status.Replicas),
			Rollout: deploymentRollout(d),
			patch: func(ctx context.Context, body []byte, opts metav1.PatchOptions) error {
				_, err := apps.Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, opts)
				return err
			},
		})
	}

	statefulSets, err := apps.StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range statefulSets.Items {
		s := &statefulSets.Items[i]
		if why := excluded(s); why != "" {
			report("StatefulSet", s.Name, why)
			continue
		}
		if s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
			report("StatefulSet", s.Name, refusedOnDelete)
			continue
		}
		if ru := s.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil && *ru.Partition > 0 {
			report("StatefulSet", s.Name, fmt.Sprintf(
				"rollingUpdate.partition is %d: the patch would not reach the ordinals below it, so it would sit inert until the partition is lowered days afterwards (BR-04)",
				*ru.Partition))
			continue
		}
		name := s.Name
		targets = append(targets, hardeningTarget{
			Ref:     plan.Target{Namespace: namespace, Kind: "StatefulSet", Name: name},
			Pod:     &s.Spec.Template.Spec,
			Pods:    int(s.Status.Replicas),
			Rollout: statefulSetRollout(s),
			patch: func(ctx context.Context, body []byte, opts metav1.PatchOptions) error {
				_, err := apps.StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, opts)
				return err
			},
		})
	}

	daemonSets, err := apps.DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	for i := range daemonSets.Items {
		d := &daemonSets.Items[i]
		if why := excluded(d); why != "" {
			report("DaemonSet", d.Name, why)
			continue
		}
		if d.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
			report("DaemonSet", d.Name, refusedOnDelete)
			continue
		}
		name := d.Name
		targets = append(targets, hardeningTarget{
			Ref:     plan.Target{Namespace: namespace, Kind: "DaemonSet", Name: name},
			Pod:     &d.Spec.Template.Spec,
			Pods:    int(d.Status.DesiredNumberScheduled),
			Rollout: daemonSetRollout(d),
			patch: func(ctx context.Context, body []byte, opts metav1.PatchOptions) error {
				_, err := apps.DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, opts)
				return err
			},
		})
	}

	reported, err := r.discoverFindings(ctx, namespace)
	if err != nil {
		return nil, nil, err
	}
	findings = append(findings, reported...)

	// Deterministic order — namespace, kind, name — so a retry resumes
	// predictably and the log reads in the same order as the preview (FR-04).
	slices.SortFunc(targets, func(a, b hardeningTarget) int {
		return strings.Compare(a.Ref.String(), b.Ref.String())
	})
	slices.SortFunc(findings, func(a, b v1alpha1.Finding) int {
		return strings.Compare(a.Kind+"/"+a.Name, b.Kind+"/"+b.Name)
	})
	return targets, findings, nil
}

// discoverFindings enumerates everything in one namespace this tool reports
// but never patches: ReplicaSets and Jobs whose owner is the target instead,
// standalone ones that are out of scope, CronJobs, and bare pods. All four
// lists are served from the API server's watch cache — a slightly stale
// observation is still a true one, and the next resync corrects it (BR-04).
//
// Split out of discover because it shares nothing with the targeting half: it
// produces findings, never targets, and reaches four resources the patching
// path never looks at. The caller sorts the combined list, so nothing here
// depends on the order these four run in.
func (r *HardeningReconciler) discoverFindings(ctx context.Context, namespace string) ([]v1alpha1.Finding, error) {
	var findings []v1alpha1.Finding
	report := func(kind, name, reason string) {
		findings = append(findings, v1alpha1.Finding{Namespace: namespace, Kind: kind, Name: name, Reason: reason})
	}

	apps := r.Kube.AppsV1()

	replicaSets, err := apps.ReplicaSets(namespace).List(ctx, fromCache)
	if err != nil {
		return nil, err
	}
	for i := range replicaSets.Items {
		rs := &replicaSets.Items[i]
		if owner := controllerOf(rs); owner != nil {
			report("ReplicaSet", rs.Name, ownedBy(owner))
			continue
		}
		report("ReplicaSet", rs.Name, "a standalone ReplicaSet is out of scope: rare enough not to justify a fourth code path (D-08)")
	}

	jobs, err := r.Kube.BatchV1().Jobs(namespace).List(ctx, fromCache)
	if err != nil {
		return nil, err
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		reason := "spec.template is immutable after creation, so a Job is reported and never patched (BR-04, D-08)"
		if owner := controllerOf(j); owner != nil && owner.Kind == "CronJob" {
			// Named so the operator can tie this Job back to the schedule that
			// created it. The CronJob itself is reported separately below.
			reason = fmt.Sprintf(
				"created by CronJob/%s, whose template this tool does not patch (D-08)",
				owner.Name)
		}
		report("Job", j.Name, reason)
	}

	// Reported in their own right, not through the Jobs they own: a CronJob
	// between schedules owns no Job, and BR-04 requires findings to be
	// enumerated whether or not they can be acted on.
	crons, err := r.Kube.BatchV1().CronJobs(namespace).List(ctx, fromCache)
	if err != nil {
		return nil, err
	}
	for i := range crons.Items {
		report("CronJob", crons.Items[i].Name,
			"a CronJob needs a second template path and has no rollout net at all — a broken one simply fails on its next schedule (D-08)")
	}

	pods, err := r.Kube.CoreV1().Pods(namespace).List(ctx, fromCache)
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if controllerOf(p) != nil {
			// It belongs to a workload, and that workload is the target.
			// Reporting every replica would drown the status in noise.
			continue
		}
		report("Pod", p.Name, "securityContext is immutable on an existing pod, and deleting someone's workload to improve it is not a trade this tool makes (BR-04, D-09)")
	}

	return findings, nil
}

// excluded reports why obj must never be read as a target, or "" when it may
// be (BR-04). The skip annotation is checked first so an operator who set it
// sees their own reason rather than an ownership one.
func excluded(obj metav1.Object) string {
	if obj.GetAnnotations()[v1alpha1.SkipAnnotation] == "true" {
		return "annotated " + v1alpha1.SkipAnnotation + `="true": the escape hatch for a workload that needs what the policy would take away (BR-04)`
	}
	if owner := controllerOf(obj); owner != nil {
		return ownedBy(owner)
	}
	return ""
}

// ownedBy is the reason for an object whose owner is the target instead.
func ownedBy(owner *metav1.OwnerReference) string {
	return fmt.Sprintf("controlled by %s/%s: the owner is the target instead, so patching this would be undone by its controller (BR-04)",
		owner.Kind, owner.Name)
}

// controllerOf returns the controlling ownerReference, or nil. Only a
// controlling reference counts: a non-controlling one records a relationship
// without implying anyone rewrites this object's template.
func controllerOf(obj metav1.Object) *metav1.OwnerReference {
	for _, o := range obj.GetOwnerReferences() {
		if o.Controller != nil && *o.Controller {
			// Go 1.22 onwards gives each iteration its own variable, so
			// taking this address is safe.
			return &o
		}
	}
	return nil
}
