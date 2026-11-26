package pythondocuments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
)

type testEditorBridge struct {
	mu      sync.Mutex
	rootDir string
	drafts  map[string]EditorDraft
	revSeq  int
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
	// Publish atomically through Painted Wolf's fseffect door and advance editor revision.
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

func TestHostDocumentStoreRealDiskAndEditorDraftLifecycle(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()

	// Seed real disk file via fseffect.Replace (respecting durable write door).
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: rootDir, Rel: "app.toml"},
		Source:   bytes.NewReader([]byte("saved = 1\n")),
		Mode:     0o644,
	})
	testutil.FailErr(t, "seed app.toml", err)

	editor := newTestEditorBridge(rootDir)
	editor.setDraft("app.toml", []byte("draft = 2\n"))

	store := NewHostDocumentStore([]HostRootConfig{
		{ID: "root-real", Dir: rootDir},
	}, editor)

	evidence := NewEvidenceLog()
	adapter := NewOverlayAdapter(OverlayConfig{
		ExecutionID: "exec-host-real",
		Store:       store,
		Roots:       []RootSpec{{ID: "root-real", Label: "main", Default: true}},
		Evidence:    evidence,
	})

	// 1. Read app.toml sees unsaved editor draft while real disk still holds "saved = 1\n".
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
	diskBefore, err := os.ReadFile(filepath.Join(rootDir, "app.toml"))
	testutil.FailErr(t, "read disk before settle", err)
	if string(diskBefore) != "saved = 1\n" {
		t.Fatalf("disk before settle = %q, want %q", string(diskBefore), "saved = 1\n")
	}

	// 2. Write new content via same-dir temp + os.replace and also create a new disk-only file.
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

	// 3. Clean-success settlement atomically publishes both to real disk!
	report, err := adapter.Settle(ctx, true)
	testutil.FailErr(t, "Settle real host store", err)
	if len(report.Published) != 2 {
		t.Fatalf("published %d changes, want 2: %+v", len(report.Published), report.Published)
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

	store := NewHostDocumentStore([]HostRootConfig{
		{ID: "root-real", Dir: rootDir},
	}, nil)
	adapter := NewOverlayAdapter(OverlayConfig{
		Store: store,
		Roots: []RootSpec{{ID: "root-real", Label: "main", Default: true}},
	})

	// Guest reads v1 (pins disk:sha256(v1)).
	rf, errno := adapter.Open(ctx, pythonruntime.OpenRequest{Path: "plain.txt", Flags: pythonruntime.OpenRead})
	if errno != pythonruntime.ErrnoSuccess {
		t.Fatalf("Open plain.txt: %s", errno)
	}
	_ = readAllGuest(t, rf)
	_ = rf.Close()

	// External modification updates real disk to v2 before settlement.
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: rootDir, Rel: "plain.txt"},
		Source:   bytes.NewReader([]byte("external-v2")),
		Mode:     0o644,
	})
	testutil.FailErr(t, "external update plain.txt", err)

	// Guest stages an update and attempts to settle.
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
	// External disk content MUST be preserved untouched.
	diskNow, err := os.ReadFile(filepath.Join(rootDir, "plain.txt"))
	testutil.FailErr(t, "read plain.txt after conflict", err)
	if string(diskNow) != "external-v2" {
		t.Fatalf("disk content = %q, want %q", string(diskNow), "external-v2")
	}
}
