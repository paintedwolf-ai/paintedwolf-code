package pythondocuments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

type testEditorBridge struct {
	mu         sync.Mutex
	rootDir    string
	drafts     map[string]EditorDraft
	captureErr error
	revSeq     int
}

func newTestEditorBridge(rootDir string) *testEditorBridge {
	return &testEditorBridge{
		rootDir: rootDir,
		drafts:  make(map[string]EditorDraft),
	}
}

func (b *testEditorBridge) setDraft(rel string, data []byte) EditorDraft {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revSeq++
	d := EditorDraft{
		DocumentID: "ed-" + rel,
		Revision:   fmt.Sprintf("ed-r%d", b.revSeq),
		Bytes:      append([]byte(nil), data...),
		Dirty:      true,
	}
	b.drafts[rel] = d
	return d
}

func (b *testEditorBridge) CaptureDraft(_ context.Context, _, _, rel string) (EditorDraft, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.captureErr != nil {
		return EditorDraft{}, false, b.captureErr
	}
	d, ok := b.drafts[rel]
	return d, ok, nil
}

func (b *testEditorBridge) PublishDraft(_ context.Context, rootID, branch, rel string, baseRev DocumentRevision, data []byte) (DocumentRevision, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.drafts[rel]
	if !ok {
		return DocumentRevision{}, false, nil
	}
	curRev := DocumentRevision{
		RootID:           rootID,
		Branch:           branch,
		DocumentID:       cur.DocumentID,
		DocumentRevision: cur.Revision,
		DiskSHA256:       SHA256Hex(cur.Bytes),
		UnsavedDraft:     cur.Dirty,
	}
	if !baseRev.Matches(curRev) {
		return DocumentRevision{}, true, &RevisionConflictError{
			Conflict: pythonruntime.RevisionConflict{
				Target: pythonruntime.CanonicalTarget{
					Namespace:  pythonruntime.NamespaceWorkspace,
					RootID:     rootID,
					Branch:     branch,
					RelPath:    rel,
					DocumentID: cur.DocumentID,
				},
				ExpectedRevision: baseRev.Identity(),
				ActualRevision:   curRev.Identity(),
			},
		}
	}
	res, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location:     fseffect.Location{Root: b.rootDir, Rel: filepath.FromSlash(rel)},
		Source:       bytes.NewReader(data),
		Mode:         0o644,
		PreserveMode: true,
	})
	if err != nil {
		return DocumentRevision{}, true, err
	}
	b.revSeq++
	updated := EditorDraft{
		DocumentID: cur.DocumentID,
		Revision:   fmt.Sprintf("ed-r%d", b.revSeq),
		Bytes:      append([]byte(nil), data...),
		Dirty:      false,
	}
	b.drafts[rel] = updated
	return DocumentRevision{
		RootID:           rootID,
		Branch:           branch,
		DocumentID:       updated.DocumentID,
		DocumentRevision: updated.Revision,
		DiskSHA256:       res.SHA256,
	}, true, nil
}

// testMutationDoor simulates Painted Wolf's single apply-and-record
// source_write.go door in unit tests, recording every applied mutation and
// enforcing revision preconditions via fseffect.
type testMutationDoor struct {
	mu       sync.Mutex
	rootDir  string
	recorded []string
}

func (d *testMutationDoor) ApplyFile(_ context.Context, req PublishWriteRequest) (PublishResult, error) {
	rel := cleanRelPath(req.Target.RelPath)
	var conflict *pythonruntime.RevisionConflict
	res, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location:     fseffect.Location{Root: d.rootDir, Rel: filepath.FromSlash(rel)},
		Source:       bytes.NewReader(req.Bytes),
		Mode:         0o644,
		PreserveMode: !req.Create,
		BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
			info, statErr := current.Lstat()
			if os.IsNotExist(statErr) {
				if req.BaseRevision.Absent {
					return nil
				}
				conflict = &pythonruntime.RevisionConflict{Target: req.Target, ExpectedRevision: req.BaseRevision.Identity(), ActualRevision: "absent"}
				return &RevisionConflictError{Conflict: *conflict}
			}
			if statErr != nil {
				return statErr
			}
			if req.BaseRevision.Absent {
				conflict = &pythonruntime.RevisionConflict{Target: req.Target, ExpectedRevision: req.BaseRevision.Identity(), ActualRevision: "exists"}
				return &RevisionConflictError{Conflict: *conflict}
			}
			if info.IsDir() || req.BaseRevision.DiskSHA256 == "" {
				return nil
			}
			f, openErr := current.Open()
			if openErr != nil {
				return openErr
			}
			defer func() { _ = f.Close() }()
			raw, readErr := io.ReadAll(f)
			if readErr != nil {
				return readErr
			}
			actualSHA := SHA256Hex(raw)
			if actualSHA != req.BaseRevision.DiskSHA256 {
				conflict = &pythonruntime.RevisionConflict{
					Target:           req.Target,
					ExpectedRevision: req.BaseRevision.Identity(),
					ActualRevision:   "rev=disk:" + actualSHA + "|sha256=" + actualSHA,
				}
				return &RevisionConflictError{Conflict: *conflict}
			}
			return nil
		},
	})
	if err != nil {
		return PublishResult{Conflict: conflict}, err
	}
	d.mu.Lock()
	d.recorded = append(d.recorded, "apply:"+rel)
	d.mu.Unlock()
	return PublishResult{
		CommittedRevision: DocumentRevision{
			RootID:           req.Target.RootID,
			Branch:           req.Target.Branch,
			DocumentRevision: "disk:" + res.SHA256,
			DiskSHA256:       res.SHA256,
		},
		BytesWritten: res.Bytes,
	}, nil
}

func (d *testMutationDoor) RemovePath(_ context.Context, req PublishDeleteRequest) (PublishResult, error) {
	rel := cleanRelPath(req.Target.RelPath)
	if err := fseffect.Remove(fseffect.RemoveRequest{Location: fseffect.Location{Root: d.rootDir, Rel: filepath.FromSlash(rel)}}); err != nil {
		return PublishResult{}, err
	}
	d.mu.Lock()
	d.recorded = append(d.recorded, "remove:"+rel)
	d.mu.Unlock()
	return PublishResult{CommittedRevision: DocumentRevision{RootID: req.Target.RootID, Branch: req.Target.Branch, Absent: true}}, nil
}

func (d *testMutationDoor) RenamePath(ctx context.Context, req PublishMoveRequest) (PublishResult, error) {
	res, err := d.ApplyFile(ctx, PublishWriteRequest{Target: req.Target, BaseRevision: req.TargetBaseRevision, Bytes: req.Bytes, Mode: req.Mode, Create: req.TargetBaseRevision.Absent})
	if err != nil {
		return res, err
	}
	_, err = d.RemovePath(ctx, PublishDeleteRequest{Target: req.Source, BaseRevision: req.SourceBaseRevision})
	return res, err
}

func (d *testMutationDoor) MkdirPath(_ context.Context, req PublishMkdirRequest) (PublishResult, error) {
	rel := cleanRelPath(req.Target.RelPath)
	if _, err := fseffect.MkdirAll(fseffect.Location{Root: d.rootDir, Rel: filepath.FromSlash(rel)}, 0o755); err != nil {
		return PublishResult{}, err
	}
	d.mu.Lock()
	d.recorded = append(d.recorded, "mkdir:"+rel)
	d.mu.Unlock()
	return PublishResult{CommittedRevision: DocumentRevision{RootID: req.Target.RootID, Branch: req.Target.Branch, DocumentRevision: "dir:created"}}, nil
}

func TestHostDocumentStoreRealDiskAndEditorDraftLifecycle(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()

	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: rootDir, Rel: "app.toml"},
		Source:   bytes.NewReader([]byte("saved = 1\n")),
		Mode:     0o644,
	})
	testutil.FailErr(t, "seed app.toml", err)

	editor := newTestEditorBridge(rootDir)
	editor.setDraft("app.toml", []byte("draft = 2\n"))
	door := &testMutationDoor{rootDir: rootDir}

	store := NewHostDocumentStore([]HostRootConfig{
		{ID: "root-real", Dir: rootDir},
	}, editor, door)

	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-host-real",
		Store:       store,
		Roots:       []RootSpec{{ID: "root-real", Label: "main", Default: true}},
	})

	rf, errno := adapter.Open(WithParentOperationID(ctx, "sysfs-read-1"), pythonruntime.OpenRequest{
		Path:  "app.toml",
		Flags: pythonruntime.OpenRead,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open app.toml: %s", errno)
	}
	got := string(readAllGuest(t, rf))
	_ = rf.Close()
	if got != "draft = 2\n" {
		t.Fatalf("guest read %q, want %q", got, "draft = 2\n")
	}

	tmp, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  ".app.tmp.1",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open temp: %s", errno)
	}
	_, _ = tmp.Write([]byte("published = 3\n"))
	_ = tmp.Close()
	if errno := adapter.Rename(ctx, ".app.tmp.1", "app.toml"); errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Rename temp->app.toml: %s", errno)
	}

	nf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "created.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open created.txt: %s", errno)
	}
	_, _ = nf.Write([]byte("hello real disk"))
	_ = nf.Close()

	report, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle real host store", err)
	if len(report.Published) != 2 {
		t.Fatalf("published %d changes, want 2: %+v", len(report.Published), report.Published)
	}
	if len(door.recorded) != 1 || door.recorded[0] != "apply:created.txt" {
		t.Fatalf("door recorded = %v, want [apply:created.txt]", door.recorded)
	}
	diskApp, err := os.ReadFile(filepath.Join(rootDir, "app.toml"))
	testutil.FailErr(t, "read app.toml after settle", err)
	if string(diskApp) != "published = 3\n" {
		t.Fatalf("app.toml on disk = %q, want %q", string(diskApp), "published = 3\n")
	}
	diskCreated, err := os.ReadFile(filepath.Join(rootDir, "created.txt"))
	testutil.FailErr(t, "read created.txt after settle", err)
	if string(diskCreated) != "hello real disk" {
		t.Fatalf("created.txt on disk = %q, want %q", string(diskCreated), "hello real disk")
	}
}

func TestHostDocumentStoreConcurrentDiskModificationConflicts(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()

	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: rootDir, Rel: "plain.txt"},
		Source:   bytes.NewReader([]byte("v1")),
		Mode:     0o644,
	})
	testutil.FailErr(t, "seed plain.txt", err)

	door := &testMutationDoor{rootDir: rootDir}
	store := NewHostDocumentStore([]HostRootConfig{
		{ID: "root-real", Dir: rootDir},
	}, nil, door)
	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "root-real", Label: "main", Default: true}},
	})

	rf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "plain.txt", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open plain.txt: %s", errno)
	}
	_ = readAllGuest(t, rf)
	_ = rf.Close()

	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: rootDir, Rel: "plain.txt"},
		Source:   bytes.NewReader([]byte("external-v2")),
		Mode:     0o644,
	})
	testutil.FailErr(t, "external update plain.txt", err)

	wf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "plain.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenTruncate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open write plain.txt: %s", errno)
	}
	_, _ = wf.Write([]byte("stale-agent-write"))
	_ = wf.Close()

	report, err := adapter.Settle(ctx, true)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on concurrent disk edit, got %v", err)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].Target.RelPath != "plain.txt" {
		t.Fatalf("unexpected conflicts: %+v", report.Conflicts)
	}
	diskNow, err := os.ReadFile(filepath.Join(rootDir, "plain.txt"))
	testutil.FailErr(t, "read plain.txt after conflict", err)
	if string(diskNow) != "external-v2" {
		t.Fatalf("disk content = %q, want %q", string(diskNow), "external-v2")
	}
}

func TestHostDocumentStorePropagatesEditorCaptureError(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()

	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: rootDir, Rel: "guarded.txt"},
		Source:   bytes.NewReader([]byte("stale disk bytes")),
		Mode:     0o644,
	})
	testutil.FailErr(t, "seed guarded.txt", err)

	editor := newTestEditorBridge(rootDir)
	editor.captureErr = errors.New("editor document core unavailable")

	store := NewHostDocumentStore([]HostRootConfig{
		{ID: "root-real", Dir: rootDir},
	}, editor, &testMutationDoor{rootDir: rootDir})
	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "root-real", Label: "main", Default: true}},
	})

	f, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "guarded.txt",
		Flags: pythonruntime.OpenRead,
	})
	if errno != pythonruntime.ErrnoEIO {
		t.Fatalf("Open with failing editor capture = %s, want EIO (must not fall back to disk)", errno)
	}
	if f != nil {
		t.Fatal("expected nil GuestFile when editor capture fails")
	}
	if pins := adapter.Pins().Snapshot(); len(pins) != 0 {
		t.Fatalf("failed editor capture must not install a disk pin, got %+v", pins)
	}
}

func TestHostDocumentStoreStatsDirectoryWithoutEditorCapture(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(root, "config"), 0o755))
	editor := newTestEditorBridge(root)
	editor.captureErr = errors.New("directory has no editable text")
	store := NewHostDocumentStore([]HostRootConfig{{ID: "workspace", Dir: root}}, editor, nil)
	snap, errno := store.StatDocument(t.Context(), pythonruntime.CanonicalTarget{RootID: "workspace", RelPath: "config"})
	if errno != pythonruntime.ErrnoSuccess || !snap.IsDir {
		t.Fatalf("stat directory: isDir=%v, errno=%v", snap.IsDir, errno)
	}
}

func TestHostDocumentStoreFailsClosedWithoutMutationDoor(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()

	store := NewHostDocumentStore([]HostRootConfig{
		{ID: "root-real", Dir: rootDir},
	}, nil, nil)
	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "root-real", Label: "main", Default: true}},
	})

	wf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{
		Path:  "blocked.txt",
		Flags: pythonruntime.OpenWrite | pythonruntime.OpenCreate,
	})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open blocked.txt: %s", errno)
	}
	_, _ = wf.Write([]byte("must not bypass mutation door"))
	_ = wf.Close()

	_, err := adapter.Settle(ctx, true)
	if !errors.Is(err, ErrMutationDoorUnavailable) {
		t.Fatalf("Settle without MutationDoor err = %v, want ErrMutationDoorUnavailable", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, "blocked.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("blocked.txt should not exist on disk when MutationDoor is nil, statErr=%v", statErr)
	}
}
