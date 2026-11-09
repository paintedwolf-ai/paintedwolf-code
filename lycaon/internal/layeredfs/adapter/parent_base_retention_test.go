package adapter

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParentRegularBaseRequiresAndRetainsIndependentBytes(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprintf("backing_available=%t", available), func(t *testing.T) {
			_, view := prepareClonedView(t, "parent-retention", t.TempDir())
			original := []byte("published parent bytes\n")
			entry := layeredfs.ParentEntry{Kind: layeredfs.EntryRegular, Mode: 0o644}
			if available {
				entry.BackingAbs = filepath.Join(t.TempDir(), "parent")
				testutil.FailErr(t, "parent content", os.WriteFile(entry.BackingAbs, original, 0o644))
			}
			capture := &statCapture{
				basesDir: t.TempDir(),
				bases:    make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture),
				synced:   make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture),
				parents:  []layeredfs.ParentLayer{parentLookupProbe{lookup: func(layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error) { return entry, true, nil }}},
			}
			path := layeredfs.QualifiedPath{RootID: "repo", Rel: "file.txt"}
			_, err := capture.baseFor(t.Context(), view.Core, path)
			retained, captured := view.Core.CapturedBase(path)
			if !available {
				if err == nil || captured || len(capture.bases) != 0 || len(capture.synced) != 0 {
					t.Fatalf("missing bytes admitted a base: err=%v captured=%v bases=%v synced=%v", err, captured, capture.bases, capture.synced)
				}
				return
			}
			testutil.FailErr(t, "capture parent base", err)
			wantSHA := fmt.Sprintf("%x", sha256.Sum256(original))
			if !captured || retained.Layer != layeredfs.LayerParent || retained.SHA256 != wantSHA || retained.Size != int64(len(original)) {
				t.Fatalf("retained parent base = %+v captured=%v", retained, captured)
			}
			testutil.FailErr(t, "change live parent backing", os.WriteFile(entry.BackingAbs, []byte("later parent bytes\n"), 0o644))
			body, err := os.ReadFile(retained.BackingFile)
			testutil.FailErr(t, "read independent retained bytes", err)
			if string(body) != string(original) {
				t.Fatalf("retained base followed later content: %q", body)
			}
		})
	}
}

func TestParentTombstoneRemainsAnAbsentBaseInCore(t *testing.T) {
	_, view := prepareClonedView(t, "parent-tombstone", t.TempDir())
	capture := &statCapture{parents: []layeredfs.ParentLayer{parentLookupProbe{
		lookup: func(layeredfs.QualifiedPath) (layeredfs.ParentEntry, bool, error) {
			return layeredfs.ParentEntry{Kind: layeredfs.EntryAbsent}, true, nil
		},
	}}}
	path := layeredfs.QualifiedPath{RootID: "repo", Rel: "deleted.txt"}
	_, err := capture.baseFor(t.Context(), view.Core, path)
	testutil.FailErr(t, "capture tombstone", err)
	base, ok := view.Core.CapturedBase(path)
	if !ok || base.Kind != layeredfs.EntryAbsent || base.Layer != layeredfs.LayerParent {
		t.Fatalf("parent deletion lost its base identity: %+v captured=%v", base, ok)
	}
}
