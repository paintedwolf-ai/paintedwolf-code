package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func newProgressEnvelopeFixture(t *testing.T, allowProgress bool) *programtest.Fixture {
	t.Helper()
	f := programtest.New(t, programtest.Options{
		Natives: func(dir string) map[string]command.NativeOperation {
			return map[string]command.NativeOperation{"tee": teeOperation(dir)}
		},
	})
	testutil.FailErr(t, "clear initial progress", f.SetProgress(""))
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load platform tool schemas", err)
	f.Runtime.Executor.Metadata.SetToolSchemas(schemas)
	testutil.FailErr(t, "register update_progress handler", f.Runtime.Registry.Register(
		"update_progress",
		reporting.ProgressHandler(f.ProgressStore(), func(_ context.Context, id string) string { return id }, nil),
	))
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec)
	testutil.FailErr(t, "compile command effects", err)
	profileTools := map[string]bool{"command": true, "write": true, "read": true}
	if allowProgress {
		profileTools["update_progress"] = true
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{
		{ID: "fixture", Tools: profileTools, CommandEffects: classes},
	})
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	f.Runtime.Boundary = boundary
	f.Runtime.Executor.Metadata.SetPolicy(policy)
	return f
}

func TestProgramProgressEnvelopeInvalidHasZeroEffects(t *testing.T) {
	t.Run("invalid_syntax", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, true)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt; if",
			"progress_update": map[string]any{
				"content": "- [ ] Create never.txt",
			},
		})
		if err == nil {
			t.Fatal("invalid syntax accepted")
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("invalid syntax committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("invalid syntax created file: %v", statErr)
		}
		if programs := f.Programs(t); len(programs) != 0 {
			t.Fatalf("invalid syntax recorded program receipts: %v", programs)
		}
	})

	t.Run("invalid_envelope_schema", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, true)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt",
			"progress_update": map[string]any{
				"content":       "- [ ] Create never.txt",
				"unknown_field": true,
			},
		})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("invalid envelope err = %v, want TOOL_ARGS_INVALID", err)
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("invalid envelope committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("invalid envelope created file: %v", statErr)
		}
		if programs := f.Programs(t); len(programs) != 0 {
			t.Fatalf("invalid envelope recorded program receipts: %v", programs)
		}
	})

	t.Run("invalid_checklist_content", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, true)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt",
			"progress_update": map[string]any{
				"content": "not a markdown checklist",
			},
		})
		if toolrejection.AsToolReject(err) == nil {
			t.Fatalf("invalid checklist err = %v, want structured rejection", err)
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("invalid checklist committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("invalid checklist created file: %v", statErr)
		}
		if programs := f.Programs(t); len(programs) != 0 {
			t.Fatalf("invalid checklist recorded program receipts: %v", programs)
		}
	})

	t.Run("profile_without_update_progress", func(t *testing.T) {
		f := newProgressEnvelopeFixture(t, false)
		_, err := f.CallArgs(t, t.Context(), map[string]any{
			"command": "printf 'never\\n' | tee never.txt",
			"progress_update": map[string]any{
				"content": "- [ ] Create never.txt",
			},
		})
		if toolrejection.AsToolReject(err) == nil {
			t.Fatalf("disallowed progress_update err = %v, want policy rejection", err)
		}
		if got := f.Progress(t.Context()); got != "" {
			t.Fatalf("disallowed progress_update committed progress: %q", got)
		}
		if _, statErr := os.Stat(filepath.Join(f.Dir, "never.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("disallowed progress_update ran gated command: %v", statErr)
		}
	})
}

func TestProgramProgressEnvelopeSettlesOnceBeforeGatedCommand(t *testing.T) {
	f := newProgressEnvelopeFixture(t, true)
	raw, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": "printf 'created\\n' | tee result.txt",
		"progress_update": map[string]any{
			"content": "- [ ] Create result.txt",
		},
	})
	testutil.FailErr(t, "run command with progress_update", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Status != 0 || outcome.Refusal != nil {
		t.Fatalf("expected completed command after progress_update: %+v", outcome)
	}
	if outcome.ProgressUpdate == nil || outcome.ProgressUpdate.Status != "updated" || !outcome.ProgressUpdate.DocumentChanged || outcome.ProgressUpdate.Snapshot.Revision == "" {
		t.Fatalf("missing settled progress_update outcome: %+v", outcome.ProgressUpdate)
	}
	if got := f.Progress(t.Context()); got != "- [ ] Create result.txt" {
		t.Fatalf("progress store = %q", got)
	}
	body, err := os.ReadFile(filepath.Join(f.Dir, "result.txt"))
	testutil.FailErr(t, "read gated command output", err)
	if string(body) != "created\n" {
		t.Fatalf("result.txt = %q, want created\\n", body)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 20)
	testutil.FailErr(t, "read program receipt page", err)
	if page.Progress == nil || page.Progress.State != "committed" || !page.Progress.OwnerInvoked {
		t.Fatalf("expected committed progress receipt: %+v", page.Progress)
	}
	if page.Program.ProgressState != "committed" || page.Program.State != "completed" {
		t.Fatalf("unexpected program receipt state: %+v", page.Program)
	}
	// Progress settlement is terminal and cannot settle a second time.
	if dupErr := f.Recorder.SettleProgramProgress(t.Context(), invocation.ProgressSettlement{
		Identity: toolcontract.ProgramProgressIdentity{
			ProgramID:     outcome.ProgramID,
			ExecutionID:   page.Progress.ExecutionID,
			ActorID:       "fixture-person",
			SessionID:     f.Session,
			RootSessionID: page.Progress.RootSessionID,
		},
		OwnerInvoked:    true,
		State:           "committed",
		DetailReference: "output:duplicate",
	}); dupErr == nil {
		t.Fatal("duplicate progress settlement accepted")
	}
}

func TestProgramProgressEnvelopeSurvivesLaterCommandRefusal(t *testing.T) {
	f := newProgressEnvelopeFixture(t, true)
	// Restrict the profile to read-only command effects while keeping update_progress
	// allowed, so the progress update commits and the subsequent write is refused.
	readOnly, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "compile read-only effects", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{
		{ID: "fixture", Tools: map[string]bool{"command": true, "update_progress": true, "write": true, "read": true}, CommandEffects: readOnly},
	})
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	f.Runtime.Boundary = boundary
	f.Runtime.Executor.Metadata.SetPolicy(policy)

	raw, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": "printf 'blocked\\n' | tee denied.txt",
		"progress_update": map[string]any{
			"content": "- [ ] Attempt denied.txt",
		},
	})
	testutil.FailErr(t, "run refused command with progress_update", err)
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode refused program outcome", json.Unmarshal([]byte(raw), &outcome))
	if outcome.Refusal == nil || outcome.State != "rejected" {
		t.Fatalf("expected command refusal after progress commit: %+v", outcome)
	}
	if outcome.ProgressUpdate == nil || outcome.ProgressUpdate.Status != "updated" {
		t.Fatalf("expected committed progress outcome on refused program: %+v", outcome.ProgressUpdate)
	}
	if _, statErr := os.Stat(filepath.Join(f.Dir, "denied.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("refused write created denied.txt: %v", statErr)
	}
	if got := f.Progress(t.Context()); got != "- [ ] Attempt denied.txt" {
		t.Fatalf("later command refusal erased committed progress: %q", got)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 20)
	testutil.FailErr(t, "read refused program receipt", err)
	if page.Progress == nil || page.Progress.State != "committed" || page.Program.ProgressState != "committed" || page.Program.State != "rejected" {
		t.Fatalf("unexpected receipt state after later refusal: program=%+v progress=%+v", page.Program, page.Progress)
	}
}

func TestStandaloneUpdateProgressUnchangedWithEnvelopeSupport(t *testing.T) {
	f := newProgressEnvelopeFixture(t, true)
	exec := toolexecution.NewExecutor(nil, f.Runtime.Registry, "fixture")
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load schemas", err)
	exec.Metadata.SetToolSchemas(schemas)
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "fixture", SessionID: f.Session, ToolCallID: "standalone-1"}}

	firstRaw, err := exec.Invoke(t.Context(), "update_progress", map[string]any{"content": "- [ ] First item"}, tc)
	testutil.FailErr(t, "standalone create", err)
	var first struct {
		Status   string `json:"status"`
		Snapshot struct {
			Revision string   `json:"revision"`
			ItemIDs  []string `json:"item_ids"`
		} `json:"snapshot"`
	}
	testutil.FailErr(t, "decode standalone create", json.Unmarshal([]byte(firstRaw), &first))
	if first.Status != "updated" || first.Snapshot.Revision == "" || len(first.Snapshot.ItemIDs) != 1 {
		t.Fatalf("unexpected standalone create response: %s", firstRaw)
	}
	if _, err := exec.Invoke(t.Context(), "update_progress", map[string]any{"content": "- [x] First item"}, tc); toolrejection.AsToolReject(err) == nil || toolrejection.AsToolReject(err).Code != "PROGRESS_REVISION_REQUIRED" {
		t.Fatalf("missing revision err = %v, want PROGRESS_REVISION_REQUIRED", err)
	}
	secondRaw, err := exec.Invoke(t.Context(), "update_progress", map[string]any{
		"content":           "- [x] First item",
		"expected_revision": first.Snapshot.Revision,
		"item_ids":          first.Snapshot.ItemIDs,
	}, tc)
	testutil.FailErr(t, "standalone revision update", err)
	var second struct {
		Status string `json:"status"`
	}
	testutil.FailErr(t, "decode standalone update", json.Unmarshal([]byte(secondRaw), &second))
	if second.Status != "updated" || f.Progress(t.Context()) != "- [x] First item" {
		t.Fatalf("standalone update failed: status=%q store=%q", second.Status, f.Progress(t.Context()))
	}
}
