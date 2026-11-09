package adapter

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// Embedding supplies unused content operations; this probe exercises lookup precedence only.
type parentLookupProbe struct {
	layeredfs.ParentLayer
	lookup func(layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error)
}

func (p parentLookupProbe) Lookup(path layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error) {
	return p.lookup(path)
}

func TestParentLookupFailureCannotBecomeAnOlderMergeBase(t *testing.T) {
	unavailable := errors.New("parent checkpoint unavailable")
	olderLookups := 0
	capture := &statCapture{
		bases:  make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture),
		synced: make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture),
		parents: []layeredfs.ParentLayer{
			parentLookupProbe{lookup: func(layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error) {
				return layeredfs.ParentEntry{}, false, unavailable
			}},
			parentLookupProbe{lookup: func(layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error) {
				olderLookups++
				return layeredfs.ParentEntry{Kind: layeredfs.EntryFile, Mode: 0o644}, true, nil
			}},
		},
	}
	// A later layer must never become authoritative because an earlier lookup failed.
	_, err := capture.baseFor(t.Context(), nil, layeredfs.QualifiedPath{RootID: "primary", Rel: "file.txt"})
	if !errors.Is(err, unavailable) {
		t.Errorf("parent lookup failure was not preserved: %v", err)
	}
	if olderLookups != 0 {
		t.Errorf("consulted an older parent after lookup failed: %d", olderLookups)
	}
	if len(capture.bases) != 0 || len(capture.synced) != 0 {
		t.Errorf("failed lookup published a merge base: bases=%v synced=%v", capture.bases, capture.synced)
	}
}
