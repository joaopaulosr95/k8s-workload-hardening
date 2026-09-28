package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// hardUID is this feature's request UID. It is distinct from 001's uid
// constant, which is already declared in validate_test.go in this package.
const hardUID = "1a2b3c44-5d6e-7f80-9102-b3c4d5e6f708"

// newHardener builds a HardeningReconciler over a fake typed clientset seeded
// with objects. The dynamic client is filled in by the reconcile tests;
// discovery never touches it.
func newHardener(objects ...runtime.Object) *HardeningReconciler {
	return &HardeningReconciler{
		Kube: fake.NewClientset(objects...),
		Protected: map[string]bool{
			"kube-system": true, "kube-public": true, "kube-node-lease": true,
			"isolation-system": true,
		},
		Timeout: 5 * time.Second,
		Now:     func() time.Time { return time.Unix(1700000000, 0) },
	}
}

// template is a pod template with one container and nothing set, so every
// policy field is a gap.
func template(container string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: container, Image: "nginx"}}},
	}
}

func deployment(namespace, name string, mutate ...func(*appsv1.Deployment)) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.DeploymentSpec{Template: template("app")},
		Status:     appsv1.DeploymentStatus{Replicas: 3},
	}
	for _, m := range mutate {
		m(d)
	}
	return d
}

func statefulSet(namespace, name string, mutate ...func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.StatefulSetSpec{Template: template("app")},
		Status:     appsv1.StatefulSetStatus{Replicas: 1},
	}
	for _, m := range mutate {
		m(s)
	}
	return s
}

func daemonSet(namespace, name string, mutate ...func(*appsv1.DaemonSet)) *appsv1.DaemonSet {
	d := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.DaemonSetSpec{Template: template("agent")},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 3},
	}
	for _, m := range mutate {
		m(d)
	}
	return d
}

// controlledBy stamps a controlling ownerReference onto an object.
func controlledBy(kind, name string) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, Controller: &yes}
}

// refs renders the discovered targets as "Kind/name" for comparison.
func refs(targets []hardeningTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Ref.Kind+"/"+t.Ref.Name)
	}
	return out
}

// findingReason returns the reason reported against one object, or "".
func findingReason(findings []v1alpha1.Finding, kind, name string) string {
	for _, f := range findings {
		if f.Kind == kind && f.Name == name {
			return f.Reason
		}
	}
	return ""
}

// All three kinds carry the template at the same path, so all three are one
// code path (BR-04). The order is deterministic — kind, then name — so a retry
// resumes predictably and the log reads in the same order as the preview
// (FR-04).
func TestDiscoverAllThreeKinds(t *testing.T) {
	r := newHardener(
		deployment("tenant-a", "web"),
		deployment("tenant-a", "api"),
		statefulSet("tenant-a", "db"),
		daemonSet("tenant-a", "agent"),
		deployment("tenant-b", "elsewhere"),
	)

	targets, _, err := r.discover(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	want := []string{"DaemonSet/agent", "Deployment/api", "Deployment/web", "StatefulSet/db"}
	got := refs(targets)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("targets = %v, want %v", got, want)
	}

	for _, target := range targets {
		if target.Pod == nil || len(target.Pod.Containers) == 0 {
			t.Errorf("%s: template not carried", target.Ref)
		}
		if target.Rollout == "" {
			t.Errorf("%s: no rollout mechanism reported (BR-09)", target.Ref)
		}
		if target.Ref.Namespace != "tenant-a" {
			t.Errorf("%s: wrong namespace", target.Ref)
		}
	}

	// Pods affected, per BR-09: replicas for Deployment and StatefulSet, the
	// desired node count for a DaemonSet.
	for _, c := range []struct {
		ref  string
		pods int
	}{{"DaemonSet/agent", 3}, {"Deployment/api", 3}, {"StatefulSet/db", 1}} {
		for _, target := range targets {
			if target.Ref.Kind+"/"+target.Ref.Name == c.ref && target.Pods != c.pods {
				t.Errorf("%s: pods = %d, want %d", c.ref, target.Pods, c.pods)
			}
		}
	}
}

// AC-07: a paused target, an OnDelete target and a StatefulSet with
// partition > 0 are each refused with a distinct reason and never patched.
// None of them rolls the patch out to every pod, so it would sit inert and the
// workload would break at some arbitrary later moment.
func TestDiscoverRefusesConfigurationsThatDoNotRollOut(t *testing.T) {
	partition := int32(2)
	r := newHardener(
		deployment("tenant-a", "paused", func(d *appsv1.Deployment) { d.Spec.Paused = true }),
		statefulSet("tenant-a", "on-delete", func(s *appsv1.StatefulSet) {
			s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
		}),
		statefulSet("tenant-a", "partitioned", func(s *appsv1.StatefulSet) {
			s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
			}
		}),
		daemonSet("tenant-a", "ds-on-delete", func(d *appsv1.DaemonSet) {
			d.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}
		}),
		deployment("tenant-a", "fine"),
	)

	targets, findings, err := r.discover(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got := refs(targets); len(got) != 1 || got[0] != "Deployment/fine" {
		t.Fatalf("targets = %v, want only Deployment/fine", got)
	}

	for _, c := range []struct{ kind, name, wants string }{
		{"Deployment", "paused", "paused"},
		{"StatefulSet", "on-delete", "OnDelete"},
		{"StatefulSet", "partitioned", "partition"},
		{"DaemonSet", "ds-on-delete", "OnDelete"},
	} {
		reason := findingReason(findings, c.kind, c.name)
		if reason == "" {
			t.Errorf("%s/%s: not reported", c.kind, c.name)
			continue
		}
		if !strings.Contains(reason, c.wants) {
			t.Errorf("%s/%s: reason = %q, want it to mention %q", c.kind, c.name, reason, c.wants)
		}
	}

	// The reasons must be distinct per cause, not one generic "refused".
	// AC-07 names three causes; two objects refused for the same cause share
	// its text, which is the point of a reason naming the cause.
	seen := map[string]bool{}
	for _, c := range []struct{ kind, name string }{
		{"Deployment", "paused"}, {"StatefulSet", "on-delete"}, {"StatefulSet", "partitioned"},
	} {
		reason := findingReason(findings, c.kind, c.name)
		if seen[reason] {
			t.Errorf("%s/%s shares the reason %q with another cause; AC-07 requires a distinct one each", c.kind, c.name, reason)
		}
		seen[reason] = true
	}
}

// AC-12: a skip-annotated target, a ReplicaSet owned by a Deployment, a bare
// Pod and a Job are each reported with a distinct reason and none is a target.
//
// Findings are enumerated whether or not they can be acted on: a namespace
// reported as hardened while a root pod runs in it is a lie, and silence is
// how that lie gets told (BR-04).
func TestDiscoverExcludesAndReports(t *testing.T) {
	r := newHardener(
		deployment("tenant-a", "skipped", func(d *appsv1.Deployment) {
			d.Annotations = map[string]string{v1alpha1.SkipAnnotation: "true"}
		}),
		deployment("tenant-a", "operator-owned", func(d *appsv1.Deployment) {
			d.OwnerReferences = []metav1.OwnerReference{controlledBy("Widget", "my-widget")}
		}),
		deployment("tenant-a", "fine"),
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "fine-abc123", Namespace: "tenant-a",
			OwnerReferences: []metav1.OwnerReference{controlledBy("Deployment", "fine")},
		}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "standalone", Namespace: "tenant-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "fine-abc123-xyz", Namespace: "tenant-a",
			OwnerReferences: []metav1.OwnerReference{controlledBy("ReplicaSet", "fine-abc123")},
		}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "tenant-a"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "tenant-a"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: "nightly-28899", Namespace: "tenant-a",
			OwnerReferences: []metav1.OwnerReference{controlledBy("CronJob", "nightly")},
		}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "tenant-a"}},
		// Between schedules: owns no Job, and must still be reported.
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "weekly", Namespace: "tenant-a"}},
	)

	targets, findings, err := r.discover(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got := refs(targets); len(got) != 1 || got[0] != "Deployment/fine" {
		t.Fatalf("targets = %v, want only Deployment/fine", got)
	}

	for _, c := range []struct{ kind, name, wants string }{
		{"Deployment", "skipped", v1alpha1.SkipAnnotation},
		{"Deployment", "operator-owned", "Widget/my-widget"},
		{"ReplicaSet", "fine-abc123", "Deployment/fine"},
		{"ReplicaSet", "standalone", "standalone"},
		{"Pod", "bare", "immutable"},
		{"Job", "migrate", "immutable"},
		{"Job", "nightly-28899", "CronJob/nightly"},
		// Reported in their own right. "weekly" owns no Job at all, so a
		// report routed through Jobs would have missed it entirely (D-08).
		{"CronJob", "nightly", "next schedule"},
		{"CronJob", "weekly", "next schedule"},
	} {
		reason := findingReason(findings, c.kind, c.name)
		if reason == "" {
			t.Errorf("%s/%s: not reported", c.kind, c.name)
			continue
		}
		if !strings.Contains(reason, c.wants) {
			t.Errorf("%s/%s: reason = %q, want it to mention %q", c.kind, c.name, reason, c.wants)
		}
	}

	// A pod belonging to a workload is not a bare pod: that workload is the
	// target, and reporting every replica would drown the status.
	if reason := findingReason(findings, "Pod", "fine-abc123-xyz"); reason != "" {
		t.Errorf("an owned pod was reported: %q", reason)
	}
}
