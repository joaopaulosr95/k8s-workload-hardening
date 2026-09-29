package plan

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

var target = Target{Namespace: "tenant-a", Kind: "Deployment", Name: "api"}

// unsorted returns three changes in an order no sort would produce, so a
// canonical form that trusts its input fails here.
func unsorted() []Change {
	return []Change{
		{Path: "spec.template.spec.securityContext.seccompProfile.type", Value: "RuntimeDefault", JSON: "RuntimeDefault"},
		{Path: "spec.template.spec.containers[app].resources.requests.cpu", Value: "10m", JSON: "10m"},
		{Path: "spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation", Value: "false", JSON: false},
	}
}

// BR-07's canonical form, stated exactly: the identity line, then one sorted
// path=value line per change, each terminated by a newline.
func TestCanonicalForm(t *testing.T) {
	want := "Deployment/tenant-a/api\n" +
		"spec.template.spec.containers[app].resources.requests.cpu=10m\n" +
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation=false\n" +
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault\n"

	if got := Canonical(target, unsorted()); got != want {
		t.Errorf("Canonical =\n%q\nwant\n%q", got, want)
	}
}

// The identity is inside the hash, so two targets needing exactly the same
// fields do not share one — otherwise approving one would approve the other,
// and approving a subset deliberately, the normal way to use a gate like this,
// would be impossible (BR-07).
func TestHashIncludesTheTarget(t *testing.T) {
	changes := unsorted()
	a := Hash(Target{Namespace: "tenant-a", Kind: "Deployment", Name: "api"}, changes)
	b := Hash(Target{Namespace: "tenant-a", Kind: "Deployment", Name: "web"}, changes)
	c := Hash(Target{Namespace: "tenant-b", Kind: "Deployment", Name: "api"}, changes)
	d := Hash(Target{Namespace: "tenant-a", Kind: "StatefulSet", Name: "api"}, changes)

	for _, pair := range []struct {
		name string
		x, y string
	}{
		{"different name", a, b},
		{"different namespace", a, c},
		{"different kind", a, d},
	} {
		if pair.x == pair.y {
			t.Errorf("%s: hashes collide (%s)", pair.name, pair.x)
		}
	}

	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(a) {
		t.Errorf("hash = %q, want twelve lowercase hex digits", a)
	}
}

// A change to any value, not just to the set of paths, moves the hash: the
// guarantee is that the set of fields to be written and their values is
// unchanged since the operator saw it (BR-07).
func TestHashMovesWithAValue(t *testing.T) {
	before := []Change{{Path: "spec.template.spec.containers[app].resources.requests.cpu", Value: "10m", JSON: "10m"}}
	after := []Change{{Path: "spec.template.spec.containers[app].resources.requests.cpu", Value: "20m", JSON: "20m"}}

	if Hash(target, before) == Hash(target, after) {
		t.Error("the hash did not move when the value written changed")
	}
	if Hash(target, before) == Hash(target, nil) {
		t.Error("an empty change set hashes the same as a non-empty one")
	}
}

// The provenance annotation is the canonical lines without the identity: the
// leaf path of every field written and the value written to it (BR-08).
func TestProvenanceIsTheLeafPaths(t *testing.T) {
	want := "spec.template.spec.containers[app].resources.requests.cpu=10m\n" +
		"spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation=false\n" +
		"spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault"

	if got := Provenance(unsorted()); got != want {
		t.Errorf("Provenance =\n%q\nwant\n%q", got, want)
	}

	// Leaf paths, not the shallowest path created. Recording "resources"
	// rather than "resources.requests.cpu" would make an undo delete a limits
	// block a human added afterwards.
	for _, line := range strings.Split(Provenance(unsorted()), "\n") {
		if strings.HasSuffix(line, "resources=") || strings.HasSuffix(line, "securityContext=") {
			t.Errorf("a non-leaf path reached the annotation: %q", line)
		}
	}
}

// Review Focus 1: the change hash must not move between two passes over an
// unchanged target. Everything feeding it is a map — the requests, the
// securityContext, the container lists — and Go randomises map iteration. A
// hash that moves makes every approval the operator copies Stale before the
// write lands, and nothing in the status says why.
//
// The loop count is deliberate: one comparison would pass by chance on a map
// small enough to iterate in insertion order.
func TestHashIsStableAcrossPasses(t *testing.T) {
	pod := func() *corev1.PodSpec {
		return &corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "setup"}},
			Containers:     []corev1.Container{{Name: "app"}, {Name: "sidecar"}, {Name: "proxy"}},
		}
	}

	// Two policies built from separately constructed maps, so the map
	// literals have independent internal layouts.
	first := Request{Requests: requests("10m", "32Mi"), ReadOnlyRootFilesystem: true}
	second := Request{Requests: requests("10m", "32Mi"), ReadOnlyRootFilesystem: true}

	want := Hash(target, Build(pod(), first).Changes)
	wantCanonical := Canonical(target, Build(pod(), first).Changes)
	wantBody, err := Patch(Build(pod(), first).Changes)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	for i := range 200 {
		p := Build(pod(), second)

		if got := Hash(target, p.Changes); got != want {
			t.Fatalf("pass %d: hash = %s, want %s — the serialisation depends on map iteration order", i, got, want)
		}
		if got := Canonical(target, p.Changes); got != wantCanonical {
			t.Fatalf("pass %d: canonical form moved:\n%q\nwant\n%q", i, got, wantCanonical)
		}
		body, err := Patch(p.Changes)
		if err != nil {
			t.Fatalf("pass %d: Patch: %v", i, err)
		}
		if !bytes.Equal(body, wantBody) {
			t.Fatalf("pass %d: patch body moved:\n%s\nwant\n%s", i, body, wantBody)
		}
	}
}

// Sorting is over whole rendered lines, not over paths only, so two changes
// that share a path prefix order the same way every time.
func TestLinesAreSortedWhole(t *testing.T) {
	changes := []Change{
		{Path: "b", Value: "1"},
		{Path: "a", Value: "2"},
		{Path: "a", Value: "1"},
	}
	got := Lines(changes)
	want := []string{"a=1", "a=2", "b=1"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Lines = %v, want %v", got, want)
			break
		}
	}
}

// The patch body: a strategic merge patch keyed by container name, carrying
// only the containers with gaps and only the fields being filled, with the
// provenance annotation in the same document (FR-02, BR-08, AC-11).
func TestPatchBody(t *testing.T) {
	pod := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup"}},
		Containers: []corev1.Container{
			{Name: "app"},
			// Already hardened and already requesting: it must not appear.
			{
				Name: "sidecar",
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{Requests: requests("1m", "8Mi")},
			},
		},
	}

	body, err := Patch(Build(pod, basic()).Changes)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("the patch is not valid JSON: %v\n%s", err, body)
	}

	// No limits key anywhere in the document (AC-04).
	if bytes.Contains(body, []byte(`"limits"`)) {
		t.Errorf("the patch contains a limits key:\n%s", body)
	}

	// The provenance annotation rides in the same request (BR-08, NFR-02).
	metadata, _ := got["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	filled, ok := annotations[v1alpha1.FilledAnnotation].(string)
	if !ok {
		t.Fatalf("no %s annotation in the patch:\n%s", v1alpha1.FilledAnnotation, body)
	}
	if filled != Provenance(Build(pod, basic()).Changes) {
		t.Errorf("annotation = %q, want the provenance lines", filled)
	}
	for _, want := range []string{
		"spec.template.spec.securityContext.runAsNonRoot=true",
		"spec.template.spec.containers[app].resources.requests.cpu=10m",
		"spec.template.spec.initContainers[setup].securityContext.capabilities.drop=[ALL]",
	} {
		if !strings.Contains(filled, want) {
			t.Errorf("annotation missing %q:\n%s", want, filled)
		}
	}

	spec, _ := got["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)

	// Pod level: the values are typed, not stringified.
	sc, _ := podSpec["securityContext"].(map[string]any)
	if sc["runAsNonRoot"] != true {
		t.Errorf("runAsNonRoot = %#v, want the bool true", sc["runAsNonRoot"])
	}
	seccomp, _ := sc["seccompProfile"].(map[string]any)
	if seccomp["type"] != "RuntimeDefault" {
		t.Errorf("seccompProfile.type = %#v", seccomp["type"])
	}

	// Container level: keyed by name, one entry, the hardened sidecar absent.
	list, _ := podSpec["containers"].([]any)
	if len(list) != 1 {
		t.Fatalf("containers = %v, want only the one with gaps", list)
	}
	entry, _ := list[0].(map[string]any)
	if entry["name"] != "app" {
		t.Errorf("containers[0].name = %v, want app", entry["name"])
	}
	if _, restated := entry["image"]; restated {
		t.Error("the patch restates a field it is not filling")
	}
	csc, _ := entry["securityContext"].(map[string]any)
	if csc["allowPrivilegeEscalation"] != false {
		t.Errorf("allowPrivilegeEscalation = %#v, want the bool false", csc["allowPrivilegeEscalation"])
	}
	caps, _ := csc["capabilities"].(map[string]any)
	drop, _ := caps["drop"].([]any)
	if len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("capabilities.drop = %#v, want [ALL]", caps["drop"])
	}
	requestsOut, _ := entry["resources"].(map[string]any)["requests"].(map[string]any)
	if requestsOut["cpu"] != "10m" || requestsOut["memory"] != "32Mi" {
		t.Errorf("requests = %#v", requestsOut)
	}

	// initContainers is a separate list and is patched the same way (BR-02).
	initList, _ := podSpec["initContainers"].([]any)
	if len(initList) != 1 {
		t.Fatalf("initContainers = %v, want one entry", initList)
	}
	if initList[0].(map[string]any)["name"] != "setup" {
		t.Errorf("initContainers[0].name = %v, want setup", initList[0].(map[string]any)["name"])
	}
}

// A target with no gaps yields no patch and no API call, so an empty change
// set must be refused rather than turned into an empty body that would restart
// every pod to write nothing (FR-02).
func TestPatchRefusesAnEmptyChangeSet(t *testing.T) {
	if _, err := Patch(nil); err == nil {
		t.Error("Patch(nil) returned a body; a target with no gaps issues no API call")
	}
}

// An init container and a regular container of the same name cannot occur —
// Kubernetes requires names to be unique across the two lists (BR-02) — but
// the path format keeps them distinct regardless, so a bug upstream cannot
// merge one into the other.
func TestListSegmentsAreDistinctPerList(t *testing.T) {
	body, err := Patch([]Change{
		{Path: "spec.template.spec.containers[app].securityContext.allowPrivilegeEscalation", Value: "false", JSON: false},
		{Path: "spec.template.spec.initContainers[app].securityContext.allowPrivilegeEscalation", Value: "false", JSON: false},
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	podSpec := got["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if len(podSpec["containers"].([]any)) != 1 || len(podSpec["initContainers"].([]any)) != 1 {
		t.Errorf("the two lists were merged:\n%s", body)
	}
}

// A malformed path is a programming error, and it must surface as one rather
// than as a patch body that quietly writes somewhere else.
func TestPatchRejectsAMalformedPath(t *testing.T) {
	for _, path := range []string{
		"spec.template.spec.containers[app]",
		"spec.template.spec.containers[]",
	} {
		if _, err := Patch([]Change{{Path: path, Value: "x", JSON: "x"}}); err == nil {
			t.Errorf("Patch accepted the malformed path %q", path)
		}
	}
}
