package maintainability

import (
	"strings"
	"testing"
)

func TestReviewChangesExposeAddedRevisedAndRemovedDecisions(t *testing.T) {
	base := &workingTree{bodies: map[string][]byte{}}
	current := &workingTree{bodies: map[string][]byte{}}
	for _, name := range []string{"unchanged", "revised", "removed"} {
		file := reviewDirectory + "/" + name + ".json"
		base.paths = append(base.paths, file)
		base.bodies[file] = []byte("old")
	}
	for _, name := range []string{"unchanged", "revised", "added"} {
		file := reviewDirectory + "/" + name + ".json"
		current.paths = append(current.paths, file)
		current.bodies[file] = []byte("new")
		if name == "unchanged" {
			current.bodies[file] = []byte("old")
		}
	}
	notes := reviewChanges(base, current)
	if len(notes) != 3 {
		t.Fatalf("changes: %v", notes)
	}
	for i, kind := range []string{"added", "removed", "revised"} {
		if !strings.Contains(notes[i], "review "+kind+":") {
			t.Fatalf("missing %s: %v", kind, notes)
		}
	}
}

func TestReviewJSONRejectsRepeatedNestedKeys(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"boundaries":{"a":"one","a":"two"}}`, `{"a":`} {
		if uniqueReviewKeys([]byte(raw)) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if err := uniqueReviewKeys([]byte(`{"a":{"x":1},"b":{"x":2},"c":[1,2]}`)); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}
}
