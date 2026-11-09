package adapter

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// Embedding supplies unused content operations; these probes exercise parent metadata failures.
type parentLookupProbe struct {
	layeredfs.ParentLayer
	lookup func(layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error)
	list   func(layeredfs.QualifiedPath) (map[string]layeredfs.ParentEntry, error)
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
				return layeredfs.ParentEntry{Kind: layeredfs.EntryRegular, Mode: 0o644}, true, nil
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

func (p parentLookupProbe) ListDir(path layeredfs.QualifiedPath) (map[string]layeredfs.ParentEntry, error) {
	return p.list(path)
}

func TestParentFailureCannotPublishPartialCaptureAndRetryRecovers(t *testing.T) {
	for _, phase := range []string{"lookup", "nested listing"} {
		t.Run(phase, func(t *testing.T) {
			unavailable := errors.New("retained parent temporarily unreadable")
			failing := true
			parent := parentLookupProbe{
				lookup: func(layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error) {
					if failing && phase == "lookup" {
						return layeredfs.ParentEntry{}, false, unavailable
					}
					return layeredfs.ParentEntry{Kind: layeredfs.EntryDirectory, Mode: 0o755}, true, nil
				},
				list: func(path layeredfs.QualifiedPath) (map[string]layeredfs.ParentEntry, error) {
					if path.Rel == "." {
						return map[string]layeredfs.ParentEntry{"nested": {Kind: layeredfs.EntryDirectory, Mode: 0o755}}, nil
					}
					if failing && phase == "nested listing" {
						return nil, unavailable
					}
					return nil, nil
				},
			}
			roots := []PreparedRoot{{RootID: "primary"}}
			records := []statRecord{{Root: "primary", Rel: "nested", Kind: layeredfs.EntryDirectory}}
			capture, err := newStatCapture(0, records, roots, []layeredfs.ParentLayer{parent}, t.TempDir())
			if !errors.Is(err, unavailable) || capture != nil {
				t.Fatalf("failed parent admitted partial capture: capture=%v err=%v", capture, err)
			}
			// Recovery retries the same metadata operation, without replacing the parent with an older base.
			failing = false
			capture, err = newStatCapture(0, records, roots, []layeredfs.ParentLayer{parent}, t.TempDir())
			if err != nil || capture == nil {
				t.Fatalf("readable parent did not recover: capture=%v err=%v", capture, err)
			}
			if _, found := capture.current[layeredfs.QualifiedPath{RootID: "primary", Rel: "nested"}]; !found {
				t.Fatal("recovered capture lost the parent directory")
			}
		})
	}
}
