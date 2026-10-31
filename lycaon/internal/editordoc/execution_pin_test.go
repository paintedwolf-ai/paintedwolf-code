package editordoc

import (
	"context"
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

func TestPublishAgentEditsSavesHeldAgentDraftWithoutTouchingHumanDraft(t *testing.T) {
	f := newAgentFixture(t, nil)
	dir := filepath.Join(f.root, "dir")
	testutil.FailErr(t, "make directory", os.Mkdir(dir, 0o755))
	f.write(t, "dir/a.txt", "base\n")
	f.write(t, "dir/human.txt", "human-base\n")
	document := f.open(t, "dir/a.txt")
	humanDoc := f.open(t, "dir/human.txt")
	_, err := f.service.ReplaceSnapshot(t.Context(), humanDoc.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: humanDoc.Revision},
		Content:         "human-unsaved\n",
		EOL:             "lf",
	})
	testutil.FailErr(t, "human types", err)
	testutil.FailErr(t, "lock directory", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	result, err := f.service.ApplyAgentEdit(t.Context(), pinnedEdit(document, "accepted\n"))
	testutil.FailErr(t, "accept pinned edit", err)
	if result.Saved || result.PublicationError == nil || result.HeldVersionID == "" || result.Document.Draft != "accepted\n" {
		t.Fatalf("result=%+v", result)
	}
	testutil.FailErr(t, "unlock directory", os.Chmod(dir, 0o755))
	testutil.FailErr(t, "publish agent edits", f.service.VisitAgentPublications(t.Context(), f.project.ID, f.project.SourceBranch, "chat-1", 1, "call-1", publishTestDraft))
	if f.disk(t, "dir/a.txt") != "accepted\n" {
		t.Fatalf("agent edit was not published to disk: %q", f.disk(t, "dir/a.txt"))
	}
	if f.disk(t, "dir/human.txt") != "human-base\n" {
		t.Fatalf("human draft was overwritten on disk: %q", f.disk(t, "dir/human.txt"))
	}
}

func TestPublishAgentEditsPreservesMixedAndOtherSessionDrafts(t *testing.T) {
	for _, scenario := range []string{"human contribution", "another session"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
			doc := f.open(t, "a.txt")
			if scenario == "human contribution" {
				typed, err := f.service.ReplaceSnapshot(t.Context(), doc.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "one\nhuman\n", EOL: "lf"})
				testutil.FailErr(t, "human edit", err)
				doc = typed
			}
			testutil.FailErr(t, "hold save", os.Chmod(f.root, 0o555))
			t.Cleanup(func() { _ = os.Chmod(f.root, 0o755) })
			content := "agent\ntwo\n"
			if scenario == "human contribution" {
				content = "agent\nhuman\n"
			}
			edit := agentEdit(doc, content)
			if scenario == "another session" {
				edit.SessionID = "chat-2"
			}
			applied, err := f.service.ApplyAgentEdit(t.Context(), edit)
			testutil.FailErr(t, "accept held agent edit", err)
			if applied.Saved || applied.HeldVersionID == "" {
				t.Fatalf("held edit=%+v", applied)
			}
			testutil.FailErr(t, "permit save", os.Chmod(f.root, 0o755))
			testutil.FailErr(t, "publish only own pure agent drafts", f.service.VisitAgentPublications(t.Context(), f.project.ID, f.project.SourceBranch, "chat-1", 1, "command-call", publishTestDraft))
			if f.disk(t, "a.txt") != "one\ntwo\n" {
				t.Fatal("command published protected draft")
			}
			stored, err := f.store.Get(t.Context(), doc.ID)
			testutil.FailErr(t, "protected draft", err)
			if !stored.Dirty || stored.Draft != applied.Document.Draft || stored.HeldAgentVersionID != applied.HeldVersionID {
				t.Fatalf("protected draft changed=%+v", stored)
			}
		})
	}
}

func publishTestDraft(_ DocumentPathState, load func(context.Context) (*Document, func(context.Context) (*Document, error), error)) error {
	_, publish, err := load(context.Background())
	if err != nil {
		return err
	}
	_, err = publish(context.Background())
	return err
}
