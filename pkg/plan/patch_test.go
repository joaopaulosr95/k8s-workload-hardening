package plan

import (
	"regexp"
	"strings"
	"testing"
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
