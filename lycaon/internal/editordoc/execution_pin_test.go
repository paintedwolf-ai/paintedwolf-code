package editordoc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func pinnedEdit(document *Document, content string) AgentEdit {
	in := agentEdit(document, content)
	in.ToolName = "write"
	in.Pin = &ExecutionPin{Revision: document.Revision, DiskSHA256: document.BaseSHA256, Absent: document.Absent}
	return in
}

func TestPinnedEditPublishesWithoutAgentReadAtCapturedRevision(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"config.json": "{\"a\":1}\n"})
	document := f.open(t, "config.json")
	result, err := f.service.ApplyAgentEdit(t.Context(), pinnedEdit(document, "{\"a\":2}\n"))
	testutil.FailErr(t, "apply pinned edit", err)
	if !result.Saved || f.disk(t, "config.json") != "{\"a\":2}\n" {
		t.Fatalf("result=%+v disk=%q", result, f.disk(t, "config.json"))
	}
	if reads := f.service.FreezeAgentReads(f.project.ID, "chat-1"); len(reads) != 0 {
		t.Fatalf("pinned edit seeded read bases: %+v", reads)
	}
	published, current, err := f.service.PublishedThrough(t.Context(), f.project.ID, document.ID, document.Revision+1)
	testutil.FailErr(t, "publication state", err)
	if !published || current.PublishedRevision != document.Revision+1 {
		t.Fatalf("published=%v document=%+v", published, current)
	}
}

func TestPinnedEditConflictsInsteadOfRebasing(t *testing.T) {
	cases := map[string]func(t *testing.T, f agentFixture, d *Document){
		"compatible human edit": func(t *testing.T, f agentFixture, d *Document) {
			_, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "one\ntwo\nthree\nhuman\n", EOL: "lf"})
			testutil.FailErr(t, "type", err)
		},
		"external disk write": func(t *testing.T, f agentFixture, _ *Document) {
			f.write(t, "a.txt", "one\ntwo\nthree\nexternal\n")
		},
		"deleted file": func(t *testing.T, f agentFixture, _ *Document) {
			testutil.FailErr(t, "delete", os.Remove(filepath.Join(f.root, "a.txt")))
		},
	}
	for name, move := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
			document := f.open(t, "a.txt")
			edit := pinnedEdit(document, "ONE\ntwo\nthree\n")
			move(t, f, document)
			before, err := f.store.Get(t.Context(), document.ID)
			testutil.FailErr(t, "read moved document", err)
			for _, step := range []string{"preview", "apply"} {
				var conflict *ExecutionConflict
				if step == "preview" {
					_, err = f.service.PreviewAgentEdits(t.Context(), []AgentEdit{edit})
				} else {
					_, err = f.service.ApplyAgentEdit(t.Context(), edit)
				}
				if !errors.As(err, &conflict) || !errors.Is(err, ErrExecutionConflict) || conflict.Pinned != *edit.Pin || conflict.Current == *edit.Pin {
					t.Fatalf("%s: err=%v", step, err)
				}
			}
			after, err := f.store.Get(t.Context(), document.ID)
			testutil.FailErr(t, "read document", err)
			if after.Revision != before.Revision || after.Draft != before.Draft || after.HeldAgentVersionID != "" {
				t.Fatalf("conflict changed the document: %+v", after)
			}
			if raw, err := os.ReadFile(filepath.Join(f.root, "a.txt")); err == nil && string(raw) == "ONE\ntwo\nthree\n" {
				t.Fatal("stale transform published")
			}
		})
	}
}

func TestPinnedEditRefusesDifferentReviewedRevision(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	edit := pinnedEdit(document, "next\n")
	edit.ReviewedRevision = document.Revision + 1
	if _, err := f.service.ApplyAgentEdit(t.Context(), edit); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("err=%v", err)
	}
	edit = pinnedEdit(document, "next\n")
	edit.ExpectedRevision = document.Revision - 1
	if _, err := f.service.PreviewAgentEdits(t.Context(), []AgentEdit{edit}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("pinned edit anchored at another revision: %v", err)
	}
	if f.disk(t, "a.txt") != "base\n" {
		t.Fatal("refused edit published")
	}
}

func TestPublishedThroughKeepsFailedPublicationRecoverable(t *testing.T) {
	f := newAgentFixture(t, nil)
	dir := filepath.Join(f.root, "dir")
	testutil.FailErr(t, "make directory", os.Mkdir(dir, 0o755))
	f.write(t, "dir/a.txt", "base\n")
	document := f.open(t, "dir/a.txt")
	testutil.FailErr(t, "lock directory", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	result, err := f.service.ApplyAgentEdit(t.Context(), pinnedEdit(document, "accepted\n"))
	testutil.FailErr(t, "accept pinned edit", err)
	if result.Saved || result.PublicationError == nil || result.HeldVersionID == "" || result.Document.Draft != "accepted\n" {
		t.Fatalf("result=%+v", result)
	}
	published, _, err := f.service.PublishedThrough(t.Context(), f.project.ID, document.ID, document.Revision+1)
	testutil.FailErr(t, "publication state", err)
	if published || f.disk(t, "dir/a.txt") != "base\n" {
		t.Fatalf("failed save reported published: %v", published)
	}
	testutil.FailErr(t, "unlock directory", os.Chmod(dir, 0o755))
	current, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "read held document", err)
	_, err = f.service.Save(t.Context(), f.project, document.ID, "window", uuid.NewString(), "", 0, current.Revision)
	testutil.FailErr(t, "person saves accepted state", err)
	published, _, err = f.service.PublishedThrough(t.Context(), f.project.ID, document.ID, document.Revision+1)
	testutil.FailErr(t, "publication state after save", err)
	if !published || f.disk(t, "dir/a.txt") != "accepted\n" {
		t.Fatalf("save did not publish accepted state: %v %q", published, f.disk(t, "dir/a.txt"))
	}
	if _, _, err := f.service.PublishedThrough(t.Context(), "other-project", document.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project publication state: %v", err)
	}
}
