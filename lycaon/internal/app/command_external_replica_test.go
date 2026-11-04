package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/testutil"
)

// External saves use the same checkpoint transition as another editor; native
// publication still refuses to replace the person's accepted unsaved typing.
func TestExternalCommandSaveMergesWithHumanTyping(t *testing.T) {
	f, service, p := documentProgram(t)
	path := filepath.Join(f.Dir, "a.txt")
	testutil.FailErr(t, "saved checkpoint", os.WriteFile(path, []byte("one\ntwo\n"), 0644))
	document, err := service.Open(t.Context(), p, "a.txt", "r1", "", "window", nil)
	testutil.FailErr(t, "open editor", err)
	typed, err := service.ReplaceSnapshot(t.Context(), document.ID, p.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision},
		Content:         "one\nmy two\n", EOL: "lf",
	})
	testutil.FailErr(t, "person types", err)
	outside := callDocumentProgram(t, f, `python3 -c 'from pathlib import Path; Path("a.txt").write_text("ONE\ntwo\n")'`)
	if outside.Status != 0 || outside.StopCode != "" || outside.Stdout != "" || outside.Stderr != "" {
		t.Fatalf("external save=%+v", outside)
	}
	saved, err := os.ReadFile(path)
	testutil.FailErr(t, "external disk bytes", err)
	if string(saved) != "ONE\ntwo\n" {
		t.Fatalf("external disk=%q", saved)
	}
	merged, err := service.ObserveDisk(t.Context(), p, document.ID, "window")
	testutil.FailErr(t, "merge outside save", err)
	if merged.Diverged || !merged.Dirty || merged.Draft != "ONE\nmy two\n" || merged.BaseContent != "ONE\ntwo\n" || merged.Epoch != typed.Epoch {
		t.Fatalf("merged replica=%+v", merged)
	}
	repeated, err := service.ObserveDisk(t.Context(), p, document.ID, "window")
	testutil.FailErr(t, "repeat observation", err)
	if repeated.Revision != merged.Revision {
		t.Fatalf("outside save applied twice: %d != %d", repeated.Revision, merged.Revision)
	}
	refused := callDocumentProgram(t, f, "cat a.txt > a.txt")
	if refused.StopCode != "COMMAND_REVISION_CONFLICT" || refused.CompletedEffects != 0 {
		t.Fatalf("native publication=%+v", refused)
	}
	current, err := service.CurrentSnapshot(t.Context(), p.ID, document.ID)
	testutil.FailErr(t, "preserved typing", err)
	if current.Draft != merged.Draft || current.BaseContent != merged.BaseContent {
		t.Fatalf("native refusal changed replica=%+v", current)
	}
}
