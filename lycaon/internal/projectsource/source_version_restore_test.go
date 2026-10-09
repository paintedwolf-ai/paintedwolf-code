package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func retainedBeforeVersion(
	t *testing.T,
	service *SourceMutationService,
	p *Project,
	path string,
	before, after []byte,
) (string, string) {
	t.Helper()
	testutil.FailErr(t, "record retained versions", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: path,
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		SessionID: "session-1", Turn: 1, Before: before, After: after,
	}))
	walk, err := service.settlement.recorder.(*sourceledger.Store).Walk.QueryWalk(
		t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "session-1"},
		10, 0, sourceledger.CommitLens{},
	)
	testutil.FailErr(t, "query retained effect", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
		t.Fatalf("retained walk = %+v", walk.Files)
	}
	effect := walk.Files[0].Effects[0]
	return walk.Files[0].FileID, effect.BeforeVersionID
}

func TestRestoreVersionWritesExactBytesAndRecordsDerivation(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	current := []byte("current\n")
	selectedBytes := []byte{0xff, 0x00, 0x81, 0x7f}
	path := filepath.Join(rootPath, "asset.bin")
	testutil.FailErr(t, "seed current file", os.WriteFile(path, current, 0o640))
	fileID, selectedVersionID := retainedBeforeVersion(t, service, p, "asset.bin", selectedBytes, current)
	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(t.Context(), p.ID, selectedVersionID)
	testutil.FailErr(t, "read selected version", err)

	result, err := service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: fileID, RootID: p.Roots[0].ID, Path: "asset.bin",
		Base:      api.SourceTip{State: api.SourceTipStateContent, Sha256: textfile.SHA256(current)},
		SessionID: "session-2", Turn: 2,
	})
	testutil.FailErr(t, "restore selected version", err)
	if !result.Changed || result.PreviousVersionID == "" || result.SHA256 != textfile.SHA256(selectedBytes) {
		t.Fatalf("restore result = %+v", result)
	}
	restored, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if string(restored) != string(selectedBytes) {
		t.Fatalf("restored bytes = %x want %x", restored, selectedBytes)
	}
	history, err := service.settlement.recorder.(*sourceledger.Store).History.QueryFileVersions(t.Context(), p.ID, fileID, 10, 0)
	testutil.FailErr(t, "query restored history", err)
	if len(history.Versions) == 0 || history.Versions[0].DerivedFromVersionID != selectedVersionID ||
		history.Versions[0].Cause != sourceledger.CauseVersionRestore ||
		history.Versions[0].Origin != api.SourceChangeOriginUser {
		t.Fatalf("restored provenance = %+v", history.Versions)
	}
}

func TestRestoreVersionKeepsTheCurrentPathAcrossHistoricalRename(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	selectedBytes := []byte("at the old path\n")
	current := []byte("current at the new path\n")
	newPath := filepath.Join(rootPath, "new-name.txt")
	testutil.FailErr(t, "seed renamed current file", os.WriteFile(newPath, current, 0o640))
	testutil.FailErr(t, "record old-path source", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "old-name.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: selectedBytes,
	}))
	fileID, selectedVersionID, err := service.settlement.recorder.(*sourceledger.Store).History.ResolveFile(
		t.Context(), p.ID, sourcebranch.Trunk, p.Roots[0].ID, "old-name.txt",
	)
	testutil.FailErr(t, "resolve old-path version", err)
	testutil.FailErr(t, "record source rename", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "new-name.txt", FromPath: "old-name.txt", FileID: fileID,
		Op: api.SourceChangeOpRename, Origin: api.SourceChangeOriginAgent,
	}))
	testutil.FailErr(t, "record renamed current source", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "new-name.txt", FileID: fileID,
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		Before: selectedBytes, After: current,
	}))
	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(t.Context(), p.ID, selectedVersionID)
	testutil.FailErr(t, "read old-path version", err)

	result, err := service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: fileID, RootID: p.Roots[0].ID, Path: "new-name.txt",
		Base: api.SourceTip{State: api.SourceTipStateContent, Sha256: textfile.SHA256(current)},
	})
	testutil.FailErr(t, "restore old-path version", err)
	if result.Path != "new-name.txt" {
		t.Fatalf("restore path = %q", result.Path)
	}
	restored, err := os.ReadFile(newPath)
	testutil.FailErr(t, "read restored renamed file", err)
	if string(restored) != string(selectedBytes) {
		t.Fatalf("renamed restore bytes = %q", restored)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "old-name.txt")); !os.IsNotExist(err) {
		t.Fatalf("historical path was recreated: %v", err)
	}
}

func TestRestoreVersionRecreatesDeletedPathAndParents(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	content := []byte("return me\n")
	path := filepath.Join(rootPath, "gone", "note.txt")
	testutil.FailErr(t, "make parent", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "seed source", os.WriteFile(path, content, 0o640))
	fileID, selectedVersionID := retainedBeforeVersion(t, service, p, "gone/note.txt", content, []byte("newer\n"))
	testutil.FailErr(t, "remove deleted parent", os.RemoveAll(filepath.Dir(path)))
	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(t.Context(), p.ID, selectedVersionID)
	testutil.FailErr(t, "read selected version", err)

	result, err := service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: fileID, RootID: p.Roots[0].ID, Path: "gone/note.txt",
		Base: api.SourceTip{State: api.SourceTipStateAbsent},
	})
	testutil.FailErr(t, "restore deleted file", err)
	if !result.Changed {
		t.Fatalf("restore result = %+v", result)
	}
	restored, err := os.ReadFile(path)
	testutil.FailErr(t, "read recreated file", err)
	if string(restored) != string(content) {
		t.Fatalf("recreated content = %q", restored)
	}
}

func TestRestoreVersionRejectsAStaleWorkingTip(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	current := []byte("current\n")
	path := filepath.Join(rootPath, "note.txt")
	testutil.FailErr(t, "seed source", os.WriteFile(path, current, 0o640))
	fileID, selectedVersionID := retainedBeforeVersion(t, service, p, "note.txt", []byte("older\n"), current)
	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(t.Context(), p.ID, selectedVersionID)
	testutil.FailErr(t, "read selected version", err)

	_, err = service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: fileID, RootID: p.Roots[0].ID, Path: "note.txt",
		Base: api.SourceTip{State: api.SourceTipStateContent, Sha256: textfile.SHA256([]byte("stale\n"))},
	})
	if err == nil || !errors.Is(err, ErrSourceWriteConflict) {
		t.Fatalf("stale restore error = %v", err)
	}
}

func TestRestoreVersionLeavesMatchingAbsenceAbsent(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "record retained deletion", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "gone.txt",
		Op: api.SourceChangeOpDelete, Origin: api.SourceChangeOriginAgent,
		SessionID: "session-absent", Turn: 1, Before: []byte("before\n"),
	}))
	walk, err := service.settlement.recorder.(*sourceledger.Store).Walk.QueryWalk(
		t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "session-absent"},
		10, 0, sourceledger.CommitLens{},
	)
	testutil.FailErr(t, "query retained deletion", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
		t.Fatalf("retained deletion = %+v", walk.Files)
	}
	fileID := walk.Files[0].FileID
	absentVersionID := walk.Files[0].Effects[0].AfterVersionID
	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(t.Context(), p.ID, absentVersionID)
	testutil.FailErr(t, "read absent version", err)
	if selected.State != string(api.SourceTipStateAbsent) {
		t.Fatalf("selected state = %q", selected.State)
	}

	result, err := service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: fileID, RootID: p.Roots[0].ID, Path: "gone.txt",
		Base: api.SourceTip{State: api.SourceTipStateAbsent},
	})
	testutil.FailErr(t, "restore matching absence", err)
	if result.Changed {
		t.Fatalf("restore result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("matching absent restore created a file: %v", err)
	}
}

func TestRestoreVersionMakesRetainedAbsenceCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service, p, rootPath, _ := sourceMutationFixture(t)
	path := filepath.Join(rootPath, "remove-me.txt")
	current := []byte("current\n")
	testutil.FailErr(t, "seed current file", os.WriteFile(path, current, 0o640))
	testutil.FailErr(t, "record retained deletion", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "remove-me.txt",
		Op: api.SourceChangeOpDelete, Origin: api.SourceChangeOriginAgent,
		SessionID: "session-delete", Turn: 1, Before: []byte("older\n"),
	}))
	walk, err := service.settlement.recorder.(*sourceledger.Store).Walk.QueryWalk(
		t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "session-delete"},
		10, 0, sourceledger.CommitLens{},
	)
	testutil.FailErr(t, "query retained deletion", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
		t.Fatalf("retained deletion = %+v", walk.Files)
	}
	fileID := walk.Files[0].FileID
	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(
		t.Context(), p.ID, walk.Files[0].Effects[0].AfterVersionID,
	)
	testutil.FailErr(t, "read retained deletion", err)

	result, err := service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: fileID, RootID: p.Roots[0].ID, Path: "remove-me.txt",
		Base: api.SourceTip{State: api.SourceTipStateContent, Sha256: textfile.SHA256(current)},
	})
	testutil.FailErr(t, "restore retained deletion", err)
	if !result.Changed || result.State != api.SourceTipStateAbsent || result.PreviousVersionID == "" {
		t.Fatalf("restore result = %+v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("restored deletion left current file: %v", err)
	}
	history, err := service.History.State(t.Context(), p.ID)
	testutil.FailErr(t, "read deletion recovery", err)
	if history.Undo == nil {
		t.Fatal("deletion has no recovery action")
	}
}

// Baseline versions have no producing effect.
func TestRestoreVersionAcceptsAStateNoEffectProduced(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	baselineBytes := []byte("as first opened\n")
	path := filepath.Join(rootPath, "opened.txt")
	testutil.FailErr(t, "seed source", os.WriteFile(path, baselineBytes, 0o640))

	tracked, err := service.settlement.recorder.(*sourceledger.Store).TrackFile(t.Context(), sourceledger.TrackInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "opened.txt", Content: baselineBytes,
	})
	testutil.FailErr(t, "track file", err)

	edited := []byte("edited since\n")
	testutil.FailErr(t, "record edit", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "opened.txt", FileID: tracked.FileID,
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		Before: baselineBytes, After: edited,
	}))
	testutil.FailErr(t, "apply edit on disk", os.WriteFile(path, edited, 0o640))

	// The baseline is listed with no action behind it.
	history, err := service.settlement.recorder.(*sourceledger.Store).History.QueryFileVersions(t.Context(), p.ID, tracked.FileID, 10, 0)
	testutil.FailErr(t, "list versions", err)
	baseline := history.Versions[len(history.Versions)-1]
	if baseline.ID != tracked.VersionID || baseline.Op != "" || baseline.EffectID != "" {
		t.Fatalf("oldest version = %+v want the tracked baseline with no effect", baseline)
	}

	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(t.Context(), p.ID, baseline.ID)
	testutil.FailErr(t, "read baseline version", err)
	result, err := service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: tracked.FileID, RootID: p.Roots[0].ID, Path: "opened.txt",
		Base: api.SourceTip{State: api.SourceTipStateContent, Sha256: textfile.SHA256(edited)},
	})
	testutil.FailErr(t, "restore baseline version", err)
	if !result.Changed || result.SHA256 != textfile.SHA256(baselineBytes) {
		t.Fatalf("restore result = %+v", result)
	}
	restored, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if string(restored) != string(baselineBytes) {
		t.Fatalf("restored bytes = %q want %q", restored, baselineBytes)
	}
}

// A worker branch writes its own versions of a shared logical file; restoring
// one lands that branch's bytes on the project roots.
func TestRestoreVersionAcceptsAWorkerBranchVersion(t *testing.T) {
	service, p, rootPath, _ := sourceMutationFixture(t)
	primaryBytes := []byte("primary\n")
	path := filepath.Join(rootPath, "shared.txt")
	testutil.FailErr(t, "seed source", os.WriteFile(path, primaryBytes, 0o640))
	testutil.FailErr(t, "record primary", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID,
		RootID:    p.Roots[0].ID, Path: "shared.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, After: primaryBytes,
	}))
	fileID, primaryVersionID, err := service.settlement.recorder.(*sourceledger.Store).History.ResolveFile(
		t.Context(), p.ID, sourcebranch.Trunk, p.Roots[0].ID, "shared.txt")
	testutil.FailErr(t, "resolve file", err)

	workerBytes := []byte("worker draft\n")
	testutil.FailErr(t, "record worker write", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{
		ProjectID: p.ID, BranchID: "job-1",
		RootID: p.Roots[0].ID, Path: "shared.txt", FileID: fileID,
		DerivedFromVersionID: primaryVersionID, JobID: "job-1",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
		Before: primaryBytes, After: workerBytes,
	}))

	history, err := service.settlement.recorder.(*sourceledger.Store).History.QueryFileVersions(t.Context(), p.ID, fileID, 10, 0)
	testutil.FailErr(t, "list versions", err)
	worker := history.Versions[0]
	if !worker.BranchID.IsWorker() {
		t.Fatalf("newest version = %+v want the worker branch", worker)
	}

	selected, err := service.settlement.recorder.(*sourceledger.Store).History.ReadRestorableVersion(t.Context(), p.ID, worker.ID)
	testutil.FailErr(t, "read worker version", err)
	result, err := service.Versions.Restore(t.Context(), uuid.NewString(), p, SourceVersionRestoreRequest{
		Version: selected, FileID: fileID, RootID: p.Roots[0].ID, Path: "shared.txt",
		Base: api.SourceTip{State: api.SourceTipStateContent, Sha256: textfile.SHA256(primaryBytes)},
	})
	testutil.FailErr(t, "restore worker version", err)
	restored, err := os.ReadFile(path)
	testutil.FailErr(t, "read restored file", err)
	if !result.Changed || string(restored) != string(workerBytes) {
		t.Fatalf("restored bytes = %q want %q (result %+v)", restored, workerBytes, result)
	}
}
