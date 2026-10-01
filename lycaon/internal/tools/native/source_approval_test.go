package native

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestOmittedFileContentIsNotPresentedAsEmpty(t *testing.T) {
	preview := agentMutationPreview(tools.ToolContext{}, agentMutation{
		AbsPath: "/repo/file.txt", BeforeSize: 42, BeforeSHA256: "before-hash",
		After: []byte("new"), AfterSHA256: "after-hash",
	}, fseffect.Location{}, fseffect.Location{})
	if preview.PreviewNote == "" || preview.BeforeBytes != 42 || preview.BeforeSHA256 != "before-hash" {
		t.Fatalf("omitted content lost its explanation or identity: %+v", preview)
	}
}

func TestPolicyWriteWaitsForReviewAndRetainsRejectedBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	before, after := []byte("Original instructions\n"), []byte("Reviewed instructions\n")
	testutil.FailErr(t, "seed policy", os.WriteFile(path, before, 0o644))
	tc := nativefixture.Context(dir)
	refused := errors.New("review declined")
	var reviews int
	tc.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		reviews++
		current, err := os.ReadFile(path)
		testutil.FailErr(t, "read pending policy", err)
		if string(current) != string(before) {
			t.Fatal("policy changed before approval")
		}
		if len(changes) != 1 || changes[0].Preview.Before != string(before) || changes[0].Preview.After != string(after) {
			t.Fatalf("review did not receive the actual diff: %+v", changes)
		}
		if reviews == 1 {
			return refused
		}
		return nil
	}
	apply := func() error {
		return applyAgentFile(t.Context(), tc, testMutationTarget(path), after, before, textfile.SHA256(before))
	}
	if err := apply(); !errors.Is(err, refused) {
		t.Fatalf("rejected write = %v", err)
	}
	testutil.FailErr(t, "approve policy change", apply())
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read approved policy", err)
	if string(current) != string(after) || reviews != 2 {
		t.Fatalf("current=%q reviews=%d", current, reviews)
	}
}

func TestPolicyWriteWithoutReviewCannotLand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	err := applyAgentFile(t.Context(), nativefixture.Context(dir), testMutationTarget(path), []byte("instructions"), nil, "")
	if err == nil {
		t.Fatal("unwired policy write succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unapproved policy exists: %v", err)
	}
}

func TestFileReviewRechecksConcurrentChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	before := []byte("Original\n")
	testutil.FailErr(t, "seed policy", os.WriteFile(path, before, 0o644))
	tc := nativefixture.Context(dir)
	tc.FileChangeReview = func(context.Context, []tools.FileChange) error {
		_, err := fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.PathLocation(path), Source: bytes.NewBufferString("Human edit\n"), Mode: 0o644})
		return err
	}
	err := applyAgentFile(t.Context(), tc, testMutationTarget(path), []byte("Agent edit\n"), before, textfile.SHA256(before))
	if err == nil {
		t.Fatal("stale review overwrote a human edit")
	}
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read concurrent edit", err)
	if string(current) != "Human edit\n" {
		t.Fatalf("human edit lost: %q", current)
	}
}

func TestArchivePolicyEntryUsesFileReview(t *testing.T) {
	dir := t.TempDir()
	writeTestZip(t, filepath.Join(dir, "payload.zip"), map[string]string{"nested/AGENTS.md": "New instructions\n"})
	tc := nativefixture.Context(dir)
	reviewed := false
	tc.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if filepath.Base(change.Path) == "AGENTS.md" {
				reviewed = true
				if change.Preview.After != "New instructions\n" {
					t.Fatalf("archive preview = %+v", change.Preview)
				}
			}
		}
		return nil
	}
	tool := &ExtractArchiveTool{Boundary: extractTestBoundary(t)}
	_, err := tool.Run(t.Context(), map[string]any{"path": "payload.zip", "dest": "."}, tc)
	testutil.FailErr(t, "extract reviewed policy", err)
	if !reviewed {
		t.Fatal("archive bypassed policy review")
	}
}
