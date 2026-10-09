package projectsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

func installTestTrash(t *testing.T, service *SourceMutationService) string {
	t.Helper()
	dir := t.TempDir()
	service.Effects.SetTrashMover(func(_ context.Context, path string) error {
		return os.Rename(path, filepath.Join(dir, uuid.NewString()+"-"+filepath.Base(path)))
	})
	return dir
}

func TestTrashRecoverySurvivesEmptyTrashAndRestart(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	trash := installTestTrash(t, service)
	testutil.FailErr(t, "create nested folder", os.MkdirAll(filepath.Join(root, "folder", "empty"), 0o750))
	testutil.FailErr(t, "seed binary", os.WriteFile(filepath.Join(root, "folder", "data"), []byte{0, 1, 255}, 0o640))
	testutil.FailErr(t, "seed dangling link", os.Symlink("../missing", filepath.Join(root, "folder", "link")))
	before, err := sourceTreeFingerprint(t.Context(), filepath.Join(root, "folder"))
	testutil.FailErr(t, "fingerprint before", err)
	id := uuid.NewString()
	req := SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "folder", Recursive: true}
	testutil.FailErr(t, "trash folder", service.Delete(t.Context(), id, p, req))
	testutil.FailErr(t, "empty test trash", os.RemoveAll(trash))
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	installTestTrash(t, restarted)
	testutil.FailErr(t, "replay committed delete", restarted.Delete(t.Context(), id, p, req))
	undoHistoryHead(t, restarted, p, id)
	after, err := sourceTreeFingerprint(t.Context(), filepath.Join(root, "folder"))
	testutil.FailErr(t, "fingerprint restored tree", err)
	if after != before {
		t.Fatalf("restored fingerprint %s, want %s", after, before)
	}
	redoHistoryHead(t, restarted, p, id)
	undoHistoryHead(t, restarted, p, id)
}

func TestTrashFailureRestoresSourceAndRetryUsesOriginalOperation(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "file.txt"), []byte("keep"), 0o600))
	service.Effects.SetTrashMover(func(context.Context, string) error { return os.ErrPermission })
	id := uuid.NewString()
	req := SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file.txt"}
	if err := service.Delete(t.Context(), id, p, req); !errors.Is(err, ErrSourceTrashFailed) {
		t.Fatalf("trash error = %v", err)
	}
	assertSourceHistoryFile(t, root, "file.txt", "keep")
	trash := t.TempDir()
	service.Effects.SetTrashMover(func(ctx context.Context, path string) error {
		row, found, err := service.Journal.load(t.Context(), id)
		if err != nil || !found || row.Status != sourceMutationPrepared {
			t.Fatalf("retry acceptance was not durable before native effect: row=%+v found=%v err=%v", row, found, err)
		}
		return os.Rename(path, filepath.Join(trash, "file.txt"))
	})
	testutil.FailErr(t, "retry trash", service.Delete(t.Context(), id, p, req))
	undoHistoryHead(t, service, p, id)
	assertSourceHistoryFile(t, root, "file.txt", "keep")
}

func TestTrashMovesSelectedSymlinkAndPreservesExternalTarget(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	external := filepath.Join(t.TempDir(), "outside.txt")
	testutil.FailErr(t, "seed external", os.WriteFile(external, []byte("external"), 0o600))
	testutil.FailErr(t, "seed link", os.Symlink(external, filepath.Join(root, "link")))
	id := uuid.NewString()
	testutil.FailErr(t, "trash link", service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "link"}))
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatalf("link remains: %v", err)
	}
	body, err := os.ReadFile(external)
	testutil.FailErr(t, "external target remains", err)
	if string(body) != "external" {
		t.Fatalf("external target changed: %q", body)
	}
	undoHistoryHead(t, service, p, id)
	target, err := os.Readlink(filepath.Join(root, "link"))
	testutil.FailErr(t, "restored link", err)
	if target != external {
		t.Fatalf("target %q want %q", target, external)
	}
}

func TestTrashRefusesSymlinkedParentEscape(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	external := t.TempDir()
	testutil.FailErr(t, "seed external", os.WriteFile(filepath.Join(external, "keep"), []byte("keep"), 0o600))
	testutil.FailErr(t, "seed directory link", os.Symlink(external, filepath.Join(root, "outside")))
	err := service.Delete(t.Context(), uuid.NewString(), p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "outside/keep"})
	if !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("parent escape = %v", err)
	}
	assertSourceHistoryFile(t, external, "keep", "keep")
}

func TestTrashLostAcknowledgementDoesNotDeleteReplacement(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	trash := t.TempDir()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte("original"), 0o600))
	service.Effects.SetTrashMover(func(_ context.Context, path string) error {
		if err := os.Rename(path, filepath.Join(trash, "file")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "file"), []byte("replacement"), 0o600); err != nil {
			return err
		}
		return context.DeadlineExceeded
	})
	id := uuid.NewString()
	req := SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}
	testutil.FailErr(t, "trash despite lost acknowledgement", service.Delete(t.Context(), id, p, req))
	testutil.FailErr(t, "replay", service.Delete(t.Context(), id, p, req))
	assertSourceHistoryFile(t, root, "file", "replacement")
	_, err := service.Undo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: id})
	if !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("undo over replacement = %v", err)
	}
	assertSourceHistoryFile(t, root, "file", "replacement")
}

func TestTrashFailureIsNotRetriedAtStartup(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte("keep"), 0o600))
	service.Effects.SetTrashMover(func(context.Context, string) error { return os.ErrPermission })
	id := uuid.NewString()
	if err := service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}); !errors.Is(err, ErrSourceTrashFailed) {
		t.Fatalf("delete error = %v", err)
	}
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	restarted.Effects.SetTrashMover(func(context.Context, string) error { t.Fatal("startup retried a failed deletion"); return nil })
	testutil.FailErr(t, "recover", restarted.Recover(t.Context()))
	assertSourceHistoryFile(t, root, "file", "keep")
}

func TestTrashRecoveryFinishesInterruptedNativeEffect(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	abs, err := filepath.EvalSymlinks(root)
	testutil.FailErr(t, "resolve root", err)
	abs = filepath.Join(abs, "file")
	testutil.FailErr(t, "seed", os.WriteFile(abs, []byte("recover"), 0o600))
	fingerprint, err := sourceTreeFingerprint(t.Context(), abs)
	testutil.FailErr(t, "fingerprint", err)
	id := uuid.NewString()
	plan := sourceMutationPlan{Kind: "delete", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: p.Roots[0].ID, RootPath: root, Path: "file", AbsPath: abs, RecoveryID: id, TreeSHA: fingerprint, EntryKind: SourceEntryFile, Disposal: sourceDisposalTrash, Changed: true, Response: []byte(`{}`)}
	testutil.FailErr(t, "capture", service.recovery.captureRecovery(t.Context(), &plan, abs))
	row := &sourceMutationRow{ID: id, ProjectID: p.ID, Kind: "delete", Plan: plan, Status: sourceMutationPrepared, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	testutil.FailErr(t, "persist intent", service.Journal.insert(t.Context(), row))
	testutil.FailErr(t, "native effect before crash", service.Effects.applySourceDelete(t.Context(), row))
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	restarted.Effects.SetTrashMover(func(context.Context, string) error { t.Fatal("completed native effect repeated"); return nil })
	testutil.FailErr(t, "recover", restarted.Recover(t.Context()))
	undoHistoryHead(t, restarted, p, id)
	assertSourceHistoryFile(t, root, "file", "recover")
}

func TestTrashPreservesFilenameWhitespaceAndRetryAttribution(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	name := "  résumé ' [1].txt  "
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, name), []byte("exact name"), 0o600))
	id := uuid.NewString()
	req := SourceDeleteRequest{RootID: p.Roots[0].ID, Path: name, Turn: 1}
	testutil.FailErr(t, "delete", service.Delete(t.Context(), id, p, req))
	req.Turn = 2
	testutil.FailErr(t, "retry after session advances", service.Delete(t.Context(), id, p, req))
	undoHistoryHead(t, service, p, id)
	assertSourceHistoryFile(t, root, name, "exact name")
}

func TestRecoveryManifestCannotWriteThroughExternalLink(t *testing.T) {
	service := NewSourceMutationService(nil, nil)
	external := t.TempDir()
	body := []byte("bad")
	sha := sourceblob.ContentSHA(body)
	service.recovery.recoveryBytes[sha] = body
	entries := []sourceledger.RecoveryEntry{{Path: ".", Mode: uint32(os.ModeDir | 0o700)}, {Path: "escape", Mode: uint32(os.ModeSymlink | 0o777), Link: external}, {Path: "escape/payload", Mode: 0o600, SHA: sha}}
	service.recovery.recoveryEntries["recovery"] = entries
	scope, err := os.OpenRoot(t.TempDir())
	testutil.FailErr(t, "open restore root", err)
	defer func() { _ = scope.Close() }()
	plan := &sourceMutationPlan{RecoveryID: "recovery", RecoveryCount: int64(len(entries))}
	if err := service.recovery.restoreRecoveryManifest(t.Context(), plan, scope, "entry"); err == nil {
		t.Fatal("escaping manifest accepted")
	}
	if _, err := os.Lstat(filepath.Join(external, "payload")); !os.IsNotExist(err) {
		t.Fatalf("external write: %v", err)
	}
}

func TestInterruptedPreparationReleasesUnclaimedRecovery(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	abs := filepath.Join(root, "file")
	testutil.FailErr(t, "seed", os.WriteFile(abs, []byte("still here"), 0o600))
	fingerprint, err := sourceTreeFingerprint(t.Context(), abs)
	testutil.FailErr(t, "fingerprint", err)
	plan := sourceMutationPlan{ProjectID: p.ID, RootPath: root, Path: "file", RecoveryID: uuid.NewString(), TreeSHA: fingerprint}
	testutil.FailErr(t, "capture before interruption", service.recovery.captureRecovery(t.Context(), &plan, abs))
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	testutil.FailErr(t, "startup", restarted.Recover(t.Context()))
	var refs int
	testutil.FailErr(t, "count recovery references", service.Journal.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_recovery_objects WHERE recovery_id=?`, plan.RecoveryID).Scan(&refs))
	if refs != 0 {
		t.Fatalf("unclaimed recovery references = %d", refs)
	}
	assertSourceHistoryFile(t, root, "file", "still here")
}

func TestTrashRetryRefusesChangedSource(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	abs := filepath.Join(root, "file")
	testutil.FailErr(t, "seed", os.WriteFile(abs, []byte("first"), 0o600))
	service.Effects.SetTrashMover(func(context.Context, string) error { return os.ErrPermission })
	id := uuid.NewString()
	req := SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}
	if err := service.Delete(t.Context(), id, p, req); !errors.Is(err, ErrSourceTrashFailed) {
		t.Fatalf("delete = %v", err)
	}
	testutil.FailErr(t, "edit between attempts", os.WriteFile(abs, []byte("changed"), 0o600))
	service.Effects.SetTrashMover(func(context.Context, string) error { t.Fatal("retry moved changed source"); return nil })
	if err := service.Delete(t.Context(), id, p, req); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("retry = %v", err)
	}
	assertSourceHistoryFile(t, root, "file", "changed")
}

func TestTrashRetryRefusesChangedAttachedFolder(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed original", os.WriteFile(filepath.Join(root, "file"), []byte("original"), 0o600))
	service.Effects.SetTrashMover(func(context.Context, string) error { return os.ErrPermission })
	id := uuid.NewString()
	req := SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}
	if err := service.Delete(t.Context(), id, p, req); !errors.Is(err, ErrSourceTrashFailed) {
		t.Fatalf("first attempt = %v", err)
	}
	replacement := t.TempDir()
	testutil.FailErr(t, "seed replacement folder", os.WriteFile(filepath.Join(replacement, "file"), []byte("replacement"), 0o600))
	p.Roots[0].Path = replacement
	service.Effects.SetTrashMover(func(context.Context, string) error { t.Fatal("retried against a detached folder"); return nil })
	if err := service.Delete(t.Context(), id, p, req); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("retry = %v", err)
	}
	assertSourceHistoryFile(t, root, "file", "original")
	assertSourceHistoryFile(t, replacement, "file", "replacement")
}

func TestTrashPreflightFailureWaitsForExplicitRetry(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	abs, err := filepath.EvalSymlinks(root)
	testutil.FailErr(t, "resolve root", err)
	abs = filepath.Join(abs, "file")
	testutil.FailErr(t, "seed", os.WriteFile(abs, []byte("keep"), 0o600))
	fingerprint, err := sourceTreeFingerprint(t.Context(), abs)
	testutil.FailErr(t, "fingerprint", err)
	id := uuid.NewString()
	plan := sourceMutationPlan{Kind: "delete", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: p.Roots[0].ID, RootPath: root, Path: "file", AbsPath: abs, RecoveryID: id, TreeSHA: fingerprint, EntryKind: SourceEntryFile, Disposal: sourceDisposalTrash, Changed: true, Response: []byte(`{}`)}
	testutil.FailErr(t, "capture", service.recovery.captureRecovery(t.Context(), &plan, abs))
	row := &sourceMutationRow{ID: id, ProjectID: p.ID, Kind: "delete", Plan: plan, Status: sourceMutationPrepared, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	testutil.FailErr(t, "persist intent", service.Journal.insert(t.Context(), row))
	testutil.FailErr(t, "temporarily move selected file", os.Rename(abs, abs+".held"))
	if _, err := service.resume(t.Context(), row); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing selected file error = %v", err)
	}
	testutil.FailErr(t, "return selected file", os.Rename(abs+".held", abs))
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	restarted.Effects.SetTrashMover(func(context.Context, string) error { t.Fatal("startup retried a failed preflight"); return nil })
	testutil.FailErr(t, "recover", restarted.Recover(t.Context()))
	assertSourceHistoryFile(t, root, "file", "keep")
}

func TestTrashHistoryRetryRefusesChangedAttachedFolder(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "file"), []byte("keep"), 0o600))
	id := uuid.NewString()
	testutil.FailErr(t, "trash", service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}))
	undoHistoryHead(t, service, p, id)
	service.Effects.SetTrashMover(func(context.Context, string) error { return os.ErrPermission })
	retryID := uuid.NewString()
	req := SourceHistoryMutationRequest{ExpectedEntryID: id}
	if _, err := service.Redo(t.Context(), retryID, p, req); !errors.Is(err, ErrSourceTrashFailed) {
		t.Fatalf("redo error = %v", err)
	}
	p.Roots[0].Path = t.TempDir()
	service.Effects.SetTrashMover(func(context.Context, string) error { t.Fatal("retry used detached folder"); return nil })
	if _, err := service.Redo(t.Context(), retryID, p, req); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("retry error = %v", err)
	}
	assertSourceHistoryFile(t, root, "file", "keep")
}
