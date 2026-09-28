package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/joaopaulosr95/k8s-workload-hardening/pkg/apis/v1alpha1"
)

// hashLength is the number of hex digits published and approved. Twelve is
// short enough to copy by hand and long enough that a collision inside one
// plan is not a practical concern (BR-07).
const hashLength = 12

// Lines renders the changes as sorted "path=value" strings. It is the one
// rendering behind the change hash, the provenance annotation and the plan in
// status, so the three cannot disagree about what would be written.
//
// The sort is the load-bearing part. Everything upstream of here is a map —
// spec.resources.requests, securityContext, the container lists — and Go
// randomises map iteration. An unsorted serialisation produces a different
// hash on most passes, so every approval the operator copies is Stale before
// the write lands, and nothing in the status says why.
func Lines(changes []Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, c.Path+"="+c.Value)
	}
	slices.Sort(out)
	return out
}

// Canonical is the exact serialisation hashed for one target's change (BR-07):
// the target's identity on the first line, then one sorted "path=value" line
// per change, every line terminated by a single newline.
//
//	Deployment/tenant-a/api
//	spec.template.spec.containers[app].resources.requests.cpu=10m
//	spec.template.spec.securityContext.seccompProfile.type=RuntimeDefault
//
// The identity line is what keeps two workloads needing identical fields from
// sharing a hash, so an approval can never travel from one target to another
// and an operator can approve a subset deliberately.
func Canonical(t Target, changes []Change) string {
	var b strings.Builder
	b.WriteString(t.String())
	b.WriteByte('\n')
	for _, line := range Lines(changes) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// Hash is the first 12 hex digits of SHA-256 over Canonical.
//
// Per target, never per plan. A single plan-wide hash cannot converge in a
// live environment: any CI deploy touching any workload in any of up to
// sixteen namespaces moves it, so the operator re-copies the hash and is stale
// again before the write lands (BR-07).
//
// The guarantee is exact and narrower than it may read: a target is patched
// only if the set of fields to be written, and their values, is unchanged
// since the operator saw it. A target edited in a way that does not change its
// gaps — a new image, a different command — has the same hash and is still
// patched.
func Hash(t Target, changes []Change) string {
	sum := sha256.Sum256([]byte(Canonical(t, changes)))
	return hex.EncodeToString(sum[:])[:hashLength]
}

// Provenance is the value of the hardening.acme.corp/filled annotation: the
// leaf path of every field written and the value written to it, one per line.
// It is Canonical without the identity line, which is already on the object
// carrying it.
//
// Leaf paths, not the shallowest path created. Recording "resources" rather
// than "resources.requests.cpu" would make an undo delete a limits block a
// human added afterwards, and that safety property is worth more than avoiding
// a cosmetic empty object where a field is removed (BR-08).
func Provenance(changes []Change) string {
	return strings.Join(Lines(changes), "\n")
}

// Patch renders the strategic merge patch for changes, with the container
// lists keyed by name, and the provenance annotation in the same document — so
// a target is never patched without its record (BR-08, NFR-02).
//
// The body carries only the containers that have gaps and only the fields
// being filled: it never restates the container array, never reorders it and
// never mentions a container with no gaps (FR-02).
//
// A target with no gaps yields no patch and no API call, so an empty change
// set is an error rather than an empty body: issuing it would restart every
// pod of the workload to write nothing.
func Patch(changes []Change) ([]byte, error) {
	if len(changes) == 0 {
		return nil, errors.New("no changes: a target with no gaps yields no patch and no API call")
	}

	root := map[string]any{}
	for _, c := range changes {
		if err := insert(root, c.Path, c.JSON); err != nil {
			return nil, err
		}
	}

	// The annotation key carries dots and a slash, so it is set directly
	// rather than through insert's path walker.
	metadata := child(root, "metadata")
	child(metadata, "annotations")[v1alpha1.FilledAnnotation] = Provenance(changes)

	// encoding/json emits object keys sorted, and the changes are sorted
	// before they arrive, so the same plan always marshals to the same bytes.
	return json.Marshal(root)
}

// child returns m[key] as a map, creating it if it is missing or not a map.
func child(m map[string]any, key string) map[string]any {
	next, ok := m[key].(map[string]any)
	if !ok {
		next = map[string]any{}
		m[key] = next
	}
	return next
}

// insert writes value at path inside root, creating the objects on the way.
//
// A segment spelled name[key] is a list whose merge key is "name": the entry
// is found or created by that key, which is what makes the result a strategic
// merge patch of one container rather than a replacement of the whole array.
func insert(root map[string]any, path string, value any) error {
	segments := strings.Split(path, ".")
	current := root
	for i, segment := range segments {
		last := i == len(segments)-1
		field, key, isList := cutList(segment)

		if isList {
			if last {
				return fmt.Errorf("path %q ends in a list segment; a change must name a leaf field", path)
			}
			if key == "" {
				return fmt.Errorf("path %q names a list entry with an empty merge key", path)
			}
			list, _ := current[field].([]any)
			var entry map[string]any
			for _, raw := range list {
				if m, ok := raw.(map[string]any); ok && m["name"] == key {
					entry = m
					break
				}
			}
			if entry == nil {
				entry = map[string]any{"name": key}
				current[field] = append(list, entry)
			}
			current = entry
			continue
		}

		if last {
			current[field] = value
			return nil
		}
		current = child(current, field)
	}
	return nil
}

// cutList splits "containers[app]" into "containers", "app", true, and any
// other segment into itself, "", false.
func cutList(segment string) (field, key string, ok bool) {
	open := strings.IndexByte(segment, '[')
	if open < 0 || !strings.HasSuffix(segment, "]") {
		return segment, "", false
	}
	return segment[:open], segment[open+1 : len(segment)-1], true
}
