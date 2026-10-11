package projectsource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Progress accounts for the complete byte work in every validation phase.
type lifecycleWork struct {
	last     SourceProgress
	verified int64
}

func (w *lifecycleWork) observe(p SourceProgress) {
	if w.last.Phase == "verifying" && (p.Phase != "verifying" || (p.Entries == 0 && p.Bytes == 0)) {
		w.verified += w.last.Bytes
	}
	w.last = p
}

func (w *lifecycleWork) verificationBytes() int64 {
	if w.last.Phase == "verifying" {
		return w.verified + w.last.Bytes
	}
	return w.verified
}

func TestSourceLifecycleValidationWorkIsBounded(t *testing.T) {
	for _, operation := range []string{"trash", "copy", "move", "restore"} {
		t.Run(operation, func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			testutil.FailErr(t, "create source tree", os.Mkdir(filepath.Join(root, "tree"), 0o700))
			var total int64
			for i, size := range []int{0, 137, 2 << 20} {
				body := bytes.Repeat([]byte{byte(i)}, size)
				total += int64(size)
				testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "tree", fmt.Sprint(i)), body, 0o600))
			}
			var work lifecycleWork
			ctx := WithSourceProgress(t.Context(), work.observe)
			id := uuid.NewString()
			budget := total
			switch operation {
			case "trash":
				budget = 0
				testutil.FailErr(t, "trash tree", service.Delete(ctx, id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "tree", Recursive: true}))
			case "copy":
				_, err := service.Copy(ctx, id, p, SourceCopyRequest{RootID: p.Roots[0].ID, From: "tree", To: "copy"})
				testutil.FailErr(t, "copy tree", err)
				budget = 2 * total
			case "move":
				service.Effects.sameFilesystem = func(string, string) (bool, error) { return false, nil }
				_, err := service.Rename(ctx, id, p, SourceRenameRequest{RootID: p.Roots[0].ID, From: "tree", To: "moved"})
				testutil.FailErr(t, "move tree", err)
				budget = 2 * total
			case "restore":
				budget = 0
				testutil.FailErr(t, "trash tree", service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "tree", Recursive: true}))
				_, err := service.Undo(ctx, uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: id})
				testutil.FailErr(t, "restore tree", err)
			}
			if got := work.verificationBytes(); got != budget {
				t.Fatalf("validation read %d bytes, expected %d", got, budget)
			}
		})
	}
}

func TestRetainedTrashRefusesChangesAfterCapture(t *testing.T) {
	for _, change := range []string{"contents", "new child", "removed child", "mode"} {
		t.Run(change, func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			tree := filepath.Join(root, "tree")
			testutil.FailErr(t, "create tree", os.Mkdir(tree, 0o700))
			testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(tree, "file"), []byte("retained"), 0o600))
			service.Effects.SetTrashMover(func(context.Context, string) (desktoptrash.Receipt, error) {
				t.Fatal("changed tree reached Trash")
				return desktoptrash.Receipt{}, nil
			})
			changed := false
			ctx := WithSourceProgress(t.Context(), func(progress SourceProgress) {
				if changed || progress.Phase != "verifying" {
					return
				}
				changed = true
				switch change {
				case "contents":
					testutil.FailErr(t, "edit child", os.WriteFile(filepath.Join(tree, "file"), []byte("concurrent"), 0o600))
				case "new child":
					testutil.FailErr(t, "add child", os.WriteFile(filepath.Join(tree, "new"), []byte("new"), 0o600))
				case "removed child":
					testutil.FailErr(t, "remove child", os.Remove(filepath.Join(tree, "file")))
				case "mode":
					testutil.FailErr(t, "change child mode", os.Chmod(filepath.Join(tree, "file"), 0o400))
				}
			})
			err := legacyTrashContext(ctx, t, service, uuid.NewString(), p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "tree", Recursive: true})
			if !errors.Is(err, ErrSourceMutationDiverged) {
				t.Fatalf("changed tree accepted: %v", err)
			}
			if _, err := os.Stat(tree); err != nil {
				t.Fatalf("changed tree lost: %v", err)
			}
		})
	}
}

func TestSourceCopyRetainsCompleteTreeAndBoundedHistory(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	body := bytes.Repeat([]byte("x"), sourceledger.MaxRevisionContentBytes+1)
	testutil.FailErr(t, "seed large file", os.WriteFile(filepath.Join(root, "file"), body, 0o600))
	id := uuid.NewString()
	_, err := service.Copy(t.Context(), id, p, SourceCopyRequest{RootID: p.Roots[0].ID, From: "file", To: "copy"})
	testutil.FailErr(t, "copy large file", err)
	row, _, err := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "load copy", err)
	if len(row.Plan.After) != 0 || row.Plan.AfterSize != int64(len(body)) || row.Plan.AfterSHA == "" {
		t.Fatal("large file history lost identity or retained an unbounded body")
	}
	undoHistoryHead(t, service, p, id)
	redoHistoryHead(t, service, p, id)
	got, err := os.ReadFile(filepath.Join(root, "copy"))
	testutil.FailErr(t, "read restored copy", err)
	if !bytes.Equal(got, body) {
		t.Fatal("recovery lost large file bytes")
	}
}

func TestSourceCopyRejectsChangedSourceAndPrivateStage(t *testing.T) {
	for _, target := range []string{"source", "stage"} {
		t.Run(target, func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "file"), []byte("before"), 0o600))
			id := uuid.NewString()
			changed := false
			ctx := WithSourceProgress(t.Context(), func(progress SourceProgress) {
				if changed || progress.Phase != "verifying" {
					return
				}
				changed = true
				path := filepath.Join(root, "file")
				if target == "stage" {
					path = filepath.Join(root, ".paintedwolf-copy-"+id, "entry")
				}
				testutil.FailErr(t, "change prepared input", os.WriteFile(path, []byte("concurrent"), 0o600))
			})
			_, err := service.Copy(ctx, id, p, SourceCopyRequest{RootID: p.Roots[0].ID, From: "file", To: "copy"})
			if !errors.Is(err, ErrSourceMutationDiverged) {
				t.Fatalf("changed copy accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "copy")); !os.IsNotExist(err) {
				t.Fatalf("unverified copy published: %v", err)
			}
		})
	}
}
