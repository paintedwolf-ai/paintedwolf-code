package upgradefixture

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
)

// publishWorkerCheckpoint stores a checkpoint over roots that changed README.md and added new.txt.
func publishWorkerCheckpoint(t *testing.T, roots ...string) (storeDir string) {
	t.Helper()
	storeDir = filepath.Join(t.TempDir(), "checkpoints")
	store, err := checkpoint.NewStore(storeDir)
	testutil.FailErr(t, "open checkpoint store", err)
	blob := func(body string) string {
		sha, _, err := store.WriteBlobBytes([]byte(body))
		testutil.FailErr(t, "store blob", err)
		return sha
	}
	path := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: roots[0], Rel: rel} }
	manifest := checkpoint.Manifest{CheckpointID: "cp-retained", WorkerID: "worker", Entries: []checkpoint.ManifestEntry{
		{
			Path: path("README.md"), Kind: layeredfs.EntryRegular, ContentChanged: true,
			Final: layeredfs.ChangeFinalAttr{SHA256: blob("changed\n"), Size: 8},
			Base:  layeredfs.BaseCapture{Path: path("README.md"), Kind: layeredfs.EntryRegular, SHA256: blob("base\n"), Size: 5},
		},
		{
			Path: path("new.txt"), Kind: layeredfs.EntryRegular, ContentChanged: true,
			Final: layeredfs.ChangeFinalAttr{SHA256: blob("new\n"), Size: 4},
			Base:  layeredfs.BaseCapture{Path: path("new.txt"), Kind: layeredfs.EntryAbsent},
		},
	}}
	for _, id := range roots {
		manifest.SelectedRoots = append(manifest.SelectedRoots, layeredfs.RootSpec{ID: id, IsPrimary: id == roots[0]})
	}
	testutil.FailErr(t, "publish checkpoint", store.PublishComposedManifest(t.Context(), manifest, nil))
	return storeDir
}

// A checkpointed worker's history is read from the checkpoint it published: its base and final bodies
// must match the evidence, every evidence path must be local, and the checkpoint must hold one root.
func TestCheckpointedWorkerHistoryIsReadFromItsCheckpoint(t *testing.T) {
	evidence := func() WorkerEvidence {
		return WorkerEvidence{
			Baseline: map[string]string{"README.md": digest([]byte("base\n"))},
			Overlay:  map[string]string{"README.md": digest([]byte("changed\n")), "new.txt": digest([]byte("new\n"))},
		}
	}
	storeDir := publishWorkerCheckpoint(t, "primary")
	testutil.FailErr(t, "verify retained checkpoint", verifyCheckpointBodies(storeDir, "cp-retained", evidence()))

	for name, change := range map[string]func(*WorkerEvidence){
		"changed base":    func(e *WorkerEvidence) { e.Baseline["README.md"] = digest([]byte("other\n")) },
		"changed final":   func(e *WorkerEvidence) { e.Overlay["new.txt"] = digest([]byte("other\n")) },
		"missing body":    func(e *WorkerEvidence) { e.Overlay["absent.txt"] = digest(nil) },
		"escaping path":   func(e *WorkerEvidence) { e.Overlay["../outside"] = digest(nil) },
		"added file base": func(e *WorkerEvidence) { e.Baseline["new.txt"] = digest([]byte("new\n")) },
	} {
		t.Run(name, func(t *testing.T) {
			e := evidence()
			change(&e)
			if err := verifyCheckpointBodies(storeDir, "cp-retained", e); err == nil {
				t.Fatal("verification passed")
			}
		})
	}
	if err := verifyCheckpointBodies(storeDir, "cp-missing", evidence()); err == nil {
		t.Fatal("an unknown checkpoint verified")
	}
	if err := verifyCheckpointBodies(publishWorkerCheckpoint(t, "primary", "docs"), "cp-retained", evidence()); err == nil {
		t.Fatal("a two-root checkpoint verified as one worker topology")
	}
}
