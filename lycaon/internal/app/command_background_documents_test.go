package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramBackgroundReadServesThePersonsDraft(t *testing.T) {
	f, service, p := documentProgram(t)
	path := filepath.Join(f.Dir, "a.txt")
	testutil.FailErr(t, "saved", os.WriteFile(path, []byte("saved\n"), 0o644))
	doc, err := service.Open(t.Context(), p, "a.txt", "r1", "", "window", nil)
	testutil.FailErr(t, "open", err)
	_, err = service.ReplaceSnapshot(t.Context(), doc.ID, p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "human draft\n", EOL: "lf"})
	testutil.FailErr(t, "human edit", err)
	completed := make(chan bgprocess.Completion, 1)
	background := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c }})
	t.Cleanup(func() { _ = background.Lifecycle.Close(context.Background()) })
	f.Background, f.Runtime.Supervisor.Background = background, background

	outcome := callDocumentProgram(t, f, "{ cat a.txt; cat a.txt; } &")
	if outcome.Status != 0 || outcome.CompletedEffects != 0 {
		t.Fatalf("initial=%+v", outcome)
	}
	completion := testutil.Receive(t, "background program completes", completed)
	if completion.ExitCode != 0 || completion.SessionID != f.Session {
		t.Fatalf("completion=%+v", completion)
	}
	snapshot, err := background.Output.ReadRawOutput(f.Session, completion.Handle, 0)
	testutil.FailErr(t, "background bytes", err)
	bytes := ""
	for _, chunk := range snapshot.Output.Chunks {
		if chunk.Stream != "stdout" {
			t.Fatalf("unexpected background stream: %+v", chunk)
		}
		bytes += chunk.Text
	}
	if bytes != "human draft\nhuman draft\n" || snapshot.Output.Running || snapshot.Output.ExitCode == nil || *snapshot.Output.ExitCode != 0 {
		t.Fatalf("background bytes=%q snapshot=%+v", bytes, snapshot)
	}
	current, err := service.CurrentSnapshot(t.Context(), p.ID, doc.ID)
	testutil.FailErr(t, "draft", err)
	if !current.Dirty || current.Draft != "human draft\n" {
		t.Fatalf("draft=%+v", current)
	}
	disk, err := os.ReadFile(path)
	testutil.FailErr(t, "disk", err)
	if string(disk) != "saved\n" {
		t.Fatalf("a read saved the draft: disk=%q", disk)
	}
}
