package native

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
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
	tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
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

func TestReachedProgressAppliesOnlyToAttachedFileEdits(t *testing.T) {
	project, scratch := t.TempDir(), t.TempDir()
	tc := nativefixture.Context(project)
	checks := 0
	refused := errors.New("missing checklist")
	tc.Effects.CheckMutationProgress = func(_ context.Context, rootID, path string) error {
		checks++
		if rootID != "r1" || path != filepath.Join(project, "created.txt") {
			t.Fatalf("progress target = %s %s", rootID, path)
		}
		return refused
	}
	projectPath := filepath.Join(project, "created.txt")
	if err := applyAgentFile(t.Context(), tc, testMutationTarget(projectPath), []byte("project"), nil, ""); !errors.Is(err, refused) {
		t.Fatalf("project creation = %v, want progress refusal", err)
	}
	if checks != 1 {
		t.Fatalf("progress checks = %d, want one before review", checks)
	}
	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("project file landed after refusal: %v", err)
	}
	scratchPath := filepath.Join(scratch, "temporary.txt")
	testutil.FailErr(t, "scratch creation", applyAgentFile(t.Context(), tc, testMutationTarget(scratchPath), []byte("scratch"), nil, ""))
	if checks != 1 {
		t.Fatalf("scratch creation checked progress %d times", checks)
	}
	content, err := os.ReadFile(scratchPath)
	testutil.FailErr(t, "scratch bytes", err)
	if string(content) != "scratch" {
		t.Fatalf("scratch content = %q", content)
	}
}

func TestFileReviewRechecksConcurrentChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	before := []byte("Original\n")
	testutil.FailErr(t, "seed policy", os.WriteFile(path, before, 0o644))
	tc := nativefixture.Context(dir)
	tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
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
	tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
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

func TestCommandOutputReviewsActualBytesBeforeReplacingInstructions(t *testing.T) {
	for _, rel := range []string{"AGENTS.md", ".paintedwolf/approvals.yaml"} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, rel)
			testutil.FailErr(t, "seed policy directory", os.MkdirAll(filepath.Dir(path), 0o755))
			testutil.FailErr(t, "seed instructions", os.WriteFile(path, []byte("before\n"), 0o644))
			tc := nativefixture.Context(root)
			refused := errors.New("user declined change")
			reviewed := false
			tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
				reviewed = true
				current, err := os.ReadFile(path)
				testutil.FailErr(t, "read while output awaits review", err)
				if string(current) != "before\n" {
					t.Fatal("redirect changed destination before approval")
				}
				if len(changes) != 1 || changes[0].Preview.After != "before\nafter\n" {
					t.Fatalf("incorrect appended preview: %+v", changes)
				}
				return refused
			}
			args := map[string]any{"command": "true", "stdout_to": rel, "append": true}
			plan, err := commandsurface.ParsePlan(args)
			testutil.FailErr(t, "parse plan", err)
			params, err := command.CommandIO(t.Context(), nativefixture.Boundary(t), tc, plan, args, "command")
			testutil.FailErr(t, "prepare command output", err)
			_, err = exec.RunPipeline(t.Context(), []exec.Stage{{Name: "printf", Args: []string{"after\n"}}}, exec.ExecOpts{
				Launch: exec.HostLaunch("redirect approval fixture"), Dir: root, Redirect: params.Redirect,
			})
			if !errors.Is(err, refused) || !reviewed {
				t.Fatalf("redirect approval err=%v reviewed=%v", err, reviewed)
			}
			current, err := os.ReadFile(path)
			testutil.FailErr(t, "read declined output destination", err)
			if string(current) != "before\n" {
				t.Fatal("declined output changed instructions")
			}
		})
	}
}

func TestFileReviewCancellationStopsTheFollowingSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	before := []byte("before")
	testutil.FailErr(t, "seed reviewed file", os.WriteFile(path, before, 0o644))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tc := nativefixture.Context(root)
	reviewed := false
	tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { reviewed = true; cancel(); return nil }
	mutation := agentMutation{Op: wire.SourceChangeOpWrite, AbsPath: path, Before: before, BeforeSHA256: textfile.SHA256(before), After: []byte("after")}
	err := reviewAgentMutation(ctx, tc, mutation, testMutationTarget(path).Location, fseffect.Location{}, false)
	if !reviewed || !errors.Is(err, context.Canceled) {
		t.Fatalf("reviewed=%v outcome=%v", reviewed, err)
	}
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read canceled review target", err)
	if !bytes.Equal(got, before) {
		t.Fatalf("canceled review changed bytes=%q", got)
	}
}

func TestStagedStreamRechecksActualEntryBeforePublication(t *testing.T) {
	for _, before := range []string{"", "before"} {
		t.Run(map[bool]string{true: "missing", false: "existing"}[before == ""], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "copy")
			if before != "" {
				testutil.FailErr(t, "seed original", os.WriteFile(path, []byte(before), 0o644))
			}
			tc := nativefixture.Context(dir)
			stage, _, err := stageAgentStream(t.Context(), tc, agentStreamRequest{Target: testMutationTarget(path), Source: strings.NewReader("new bytes")})
			testutil.FailErr(t, "stage reviewed stream", err)
			defer stage.Close()
			testutil.FailErr(t, "race destination after review", os.WriteFile(path, []byte("raced"), 0o644))
			result, err := stage.Commit(t.Context(), tc)
			if !errors.Is(err, errMutationSnapshotMoved) || result.Publication.Committed() {
				t.Fatalf("changed entry published: result=%+v error=%v", result, err)
			}
			raw, err := os.ReadFile(path)
			testutil.FailErr(t, "read preserved racing bytes", err)
			if string(raw) != "raced" {
				t.Fatalf("changed entry overwritten: %q", raw)
			}
		})
	}
}
