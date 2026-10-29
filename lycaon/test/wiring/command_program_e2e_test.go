package wiring

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// commandProgramSession runs one provider-emitted command program through the
// production host and returns the delivered tool result.
type commandProgramSession struct {
	h       *Harness
	dir     string
	project string
	session string
}

// newCommandProgramSession starts a session with a progress checklist, so
// project writes pass the progress rule.
func newCommandProgramSession(t *testing.T, program, approvals string, posture ...gate.Posture) *commandProgramSession {
	return newCommandProgramSessionWithArgs(t, map[string]any{"command": program}, wire.CreateSessionRequest{}, approvals, posture...)
}

func newCommandProgramSessionWithArgs(t *testing.T, args map[string]any, request wire.CreateSessionRequest, approvals string, posture ...gate.Posture) *commandProgramSession {
	t.Helper()
	s := startCommandProgramSession(t, args, request, approvals, posture...)
	s.h.SeedProgress(t, t.Context(), s.session)
	return s
}

func newCommandProgramSessionWithoutProgress(t *testing.T, program, approvals string) *commandProgramSession {
	t.Helper()
	return startCommandProgramSession(t, map[string]any{"command": program}, wire.CreateSessionRequest{}, approvals)
}

func startCommandProgramSession(t *testing.T, args map[string]any, request wire.CreateSessionRequest, approvals string, posture ...gate.Posture) *commandProgramSession {
	t.Helper()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:      "run the program",
		ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: args}},
		FollowUpText: "Program finished.",
	}}})
	options := []Option{WithLLMClient(mock)}
	if len(posture) > 0 {
		options = append(options, withGlobalApprovalPosture(posture[0]))
	}
	h := BuildForTest(t, options...)
	dir := t.TempDir()
	testutil.FailErr(t, "seed sub dir", os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	testutil.FailErr(t, "seed notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first note\nsecond note\n"), 0o644))
	overlay := filepath.Join(dir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay", os.MkdirAll(overlay, 0o700))
	rules := "rules:\n  - category: tool\n    pattern: command\n    effect: allow\n  - category: tool\n    pattern: write\n    effect: allow\n" + approvals
	if len(posture) > 0 {
		rules = "approval_posture: " + string(posture[0]) + "\n" + rules
	}
	testutil.FailErr(t, "write approvals", os.WriteFile(filepath.Join(overlay, "approvals.yaml"), []byte(rules), 0o600))
	sess, err := h.CreateHarnessSession(t, request, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	run, err := h.Workflows.Manager.Store.Runs.ActiveBySession(t.Context(), sess.ID)
	testutil.FailErr(t, "active run", err)
	vars, err := h.Workflows.Manager.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "get scaffold vars", err)
	delete(vars, "content_review")
	testutil.FailErr(t, "clear root content_review", h.Workflows.Manager.Store.State.UpdateVars(t.Context(), run, dir, vars))
	h.Server.Admin.Capabilities.Access.Gate.PutAskQuiet(hitl.AskQuiet{
		ChatSessionID: sess.ID,
		Key:           string(wire.GateUnobservedChannel) + ":unconfined",
	}, 0)
	testutil.FailErr(t, "set agent type", h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	return &commandProgramSession{h: h, dir: dir, project: sess.ProjectID, session: sess.ID}
}

func (s *commandProgramSession) prompt(ctx context.Context) error {
	_, err := s.h.Sessions.Manager.Submissions.Prompt(ctx, s.session, "run the program")
	return err
}

// promptUntilApproval starts the prompt and returns the first pending tool
// approval the host announces for this session, with the prompt's result.
func (s *commandProgramSession) promptUntilApproval(t *testing.T) (wire.CheckpointEvent, <-chan error) {
	t.Helper()
	stream, unsubscribe, err := s.h.Events.Subscribe(t.Context(), events.Subscription{Project: s.project, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to checkpoints", err)
	defer unsubscribe()
	done := make(chan error, 1)
	go func() { done <- s.prompt(t.Context()) }()
	for {
		select {
		case err := <-done:
			t.Fatalf("prompt ended before an approval was announced: %v", err)
		case env := <-stream:
			if env.Topic != wire.EventTopicCheckpoint {
				continue
			}
			raw, err := json.Marshal(env.Data)
			testutil.FailErr(t, "encode checkpoint event", err)
			var checkpoint wire.CheckpointEvent
			testutil.FailErr(t, "decode checkpoint event", json.Unmarshal(raw, &checkpoint))
			if checkpoint.SessionID == s.session && checkpoint.Status == wire.CheckpointStatusPending && checkpoint.Kind == wire.CheckpointKindToolApproval {
				return checkpoint, done
			}
		}
	}
}

func (s *commandProgramSession) result(t *testing.T) string {
	t.Helper()
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	// The messages API carries tool output only on the projected tool result.
	for _, raw := range msgs {
		msg := messageview.RedactMessage(raw)
		if msg.Role != wire.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.ToolCallID != "program-1" {
			continue
		}
		content, _, err := messageview.RetainedContent(raw, "tool_output", "program-1")
		testutil.FailErr(t, "read projected command result", err)
		if !strings.Contains(content, `"program_id"`) {
			t.Fatalf("projected command result carries no program outcome: %q (%+v)", content, msg.ToolResult)
		}
		return content
	}
	t.Fatalf("no projected command result in %d messages", len(msgs))
	return ""
}

// executions lists child receipts as operation=state in launch order.
func (s *commandProgramSession) executions(t *testing.T) []string {
	t.Helper()
	rows, err := s.h.DB.QueryContext(t.Context(), `SELECT e.operation_id, e.state FROM invocation_program_executions e
		JOIN invocation_programs p ON p.id = e.program_id JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? AND e.operation_id NOT LIKE 'program %' ORDER BY e.ordinal`, s.session)
	testutil.FailErr(t, "read executions", err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var operation, state string
		testutil.FailErr(t, "scan execution", rows.Scan(&operation, &state))
		out = append(out, operation+"="+state)
	}
	testutil.FailErr(t, "iterate executions", rows.Err())
	return out
}

func (s *commandProgramSession) programState(t *testing.T) string {
	t.Helper()
	var state string
	testutil.FailErr(t, "read program", s.h.DB.QueryRowContext(t.Context(), `SELECT p.state FROM invocation_programs p JOIN invocation_receipts r ON r.id = p.parent_receipt_id WHERE r.session_id = ?`, s.session).Scan(&state))
	return state
}

func requireExecutions(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, entry := range want {
		found := false
		for _, row := range got {
			found = found || row == entry
		}
		if !found {
			t.Fatalf("child receipts %v lack %s", got, entry)
		}
	}
}

func TestCommandProgramNativePipelineDeliversThroughProvider(t *testing.T) {
	s := newCommandProgramSession(t, `cat notes.txt | head -n 1; grep -c note notes.txt | tr -d '\n'; printf ' %s\n' "${MISSING:-done}"`, "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	if result := s.result(t); !strings.Contains(result, "first note") || strings.Contains(result, "second note") || !strings.Contains(result, `2 done`) {
		t.Fatalf("delivered result = %q", result)
	}
	requireExecutions(t, s.executions(t), "read=completed", "grep=completed")
	if state := s.programState(t); state != "completed" {
		t.Fatalf("program state = %q", state)
	}
}

func TestCommandProgramExternalLaunchDeliversThroughProvider(t *testing.T) {
	s := newCommandProgramSession(t, "go version", "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	if result := s.result(t); !strings.Contains(result, "go version go") {
		t.Fatalf("delivered result = %q", result)
	}
	requireExecutions(t, s.executions(t), "command=completed")
}

// A redirect binds its target from the statement's directory frame and
// publishes through the native write owner.
func TestCommandProgramRedirectWritesFromStatementDirectory(t *testing.T) {
	s := newCommandProgramSession(t, "cd sub && head -n 1 ../notes.txt > copied.txt", "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	body, err := os.ReadFile(filepath.Join(s.dir, "sub", "copied.txt"))
	testutil.FailErr(t, "read redirect target", err)
	if string(body) != "first note\n" {
		t.Fatalf("redirect target = %q", body)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "copied.txt")); !os.IsNotExist(err) {
		t.Fatalf("redirect bound from the program root: %v", err)
	}
	requireExecutions(t, s.executions(t), "read=completed")
}

// An external launch that needs approval waits on a real checkpoint and runs
// only after the person approves it.
func TestCommandProgramApprovalRequiredExternalRunsAfterApproval(t *testing.T) {
	s := newCommandProgramSession(t, "go env GOOS", "  - category: command\n    pattern: \"go env*\"\n    effect: ask\n")
	ctx := t.Context()
	checkpoint, done := s.promptUntilApproval(t)
	_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(s.h.OwnerCtx(t, ctx), s.session, checkpoint.ID, "approve_current_action")
	testutil.FailErr(t, "approve launch", err)
	testutil.FailErr(t, "prompt", <-done)
	if result := s.result(t); !strings.Contains(result, "darwin") && !strings.Contains(result, "linux") {
		t.Fatalf("delivered result = %q", result)
	}
	requireExecutions(t, s.executions(t), "command=completed")
}

// Program output redirected into a credential file reaches the protected
// write review of the native write owner; declining leaves the file intact.
func TestCommandProgramRedirectIntoCredentialFileAsksAsProtectedWrite(t *testing.T) {
	for _, c := range []struct{ name, program, target string }{
		{"stdout", "printf leaked > .env", ".env"},
		{"append", "printf leaked >> .env", ".env"},
		{"pipeline stage", "printf leaked | sort > .env.production", ".env.production"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newCommandProgramSession(t, c.program, "")
			credential := filepath.Join(s.dir, c.target)
			testutil.FailErr(t, "seed credential", os.WriteFile(credential, []byte("TOKEN=SECRET\n"), 0o600))
			ctx := t.Context()
			kind := wire.CheckpointKindToolApproval
			checkpoint, done := s.promptUntilApproval(t)
			if approval := checkpoint.ToolApproval; approval == nil || !slices.Contains(approval.Plan.Reasons, wire.GateSensitiveLocation) {
				t.Fatalf("redirect into %s asked without the sensitive location gate: %+v", c.target, checkpoint)
			}
			_, err := s.h.Sessions.Checkpoints.ResolveCheckpoint(s.h.OwnerCtx(t, ctx), s.session, checkpoint.ID, kind, &hitl.DecisionResult{Approved: false}, nil)
			testutil.FailErr(t, "decline protected write", err)
			<-done
			body, err := os.ReadFile(credential)
			testutil.FailErr(t, "read credential", err)
			if string(body) != "TOKEN=SECRET\n" {
				t.Fatalf("declined review still changed %s: %q", c.target, body)
			}
		})
	}
}

// An invented capability_request key is typed argument feedback, never
// silently dropped.
func TestCommandProgramRefusesUnknownCapabilityRequestKeys(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:      "run the program",
		ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": "go version", "capability_request": map[string]any{"network": true}}}},
		FollowUpText: "Program finished.",
	}}})
	h := BuildForTest(t, WithLLMClient(mock))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "set agent type", h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "run the program")
	testutil.FailErr(t, "prompt", err)
	msgs, err := h.Store.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read transcript", err)
	for _, raw := range msgs {
		content, _, err := messageview.RetainedContent(raw, "tool_output", "program-1")
		if err != nil {
			continue
		}
		if !strings.Contains(content, "TOOL_ARGS_INVALID") || !strings.Contains(content, "network") {
			t.Fatalf("unknown capability key result: %s", content)
		}
		return
	}
	t.Fatal("no command result delivered")
}

// Child receipts carry the started job's handle, a stale destination's
// revision conflict and screened previews of each child's output.
func TestCommandProgramReceiptsCarryJobsConflictsAndPreviews(t *testing.T) {
	s := newCommandProgramSession(t, `head -n 1 notes.txt; sleep 30 & kill %1; printf 'old\n' > target.txt; sh -c 'printf changed > target.txt; printf produced' > target.txt`, "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	result := s.result(t)
	if !strings.Contains(result, "COMMAND_REVISION_CONFLICT") || !strings.Contains(result, "first note") {
		t.Fatalf("conflict program result = %q", result)
	}
	var path, expected, observed string
	testutil.FailErr(t, "read conflict receipt", s.h.DB.QueryRowContext(t.Context(), `SELECT conflict_path, conflict_expected_revision, conflict_observed_revision FROM invocation_program_executions WHERE conflict_path != ''`).Scan(&path, &expected, &observed))
	if !strings.HasSuffix(path, "target.txt") || expected == "" || observed == "" || expected == observed {
		t.Fatalf("revision conflict receipt %q %q %q", path, expected, observed)
	}
	var handle string
	testutil.FailErr(t, "read job receipt", s.h.DB.QueryRowContext(t.Context(), `SELECT job_handle FROM invocation_program_executions WHERE job_handle != ''`).Scan(&handle))
	if handle == "" {
		t.Fatal("background child has no job handle")
	}
	var preview string
	testutil.FailErr(t, "read output preview", s.h.DB.QueryRowContext(t.Context(), `SELECT preview FROM invocation_program_outputs WHERE stream = 'stdout' AND preview LIKE 'first note%'`).Scan(&preview))
	if preview != "first note\n" {
		t.Fatalf("stdout preview %q", preview)
	}
}

func TestCommandProgramLiveRun2Regressions(t *testing.T) {
	prog := `echo --- && echo --- section --- && ` +
		`printf '{"a":1}\n' | jq -r '.a' && printf 'k: v\n' | yq '.k' && ` +
		`git status -sb && git --no-pager log -n 1 --format=%s && git --no-pager diff -- notes.txt && ` +
		`printf '1\n2\n' | sed '1!G;h;$!d' && ` +
		`find . -name '*.txt' | xargs wc -l | sort -n && ` +
		`find . -name '*.txt' -print0 | xargs -0 grep -l note && ` +
		`while read -r line; do echo "row:$line"; done < notes.txt && ` +
		`(false | true; echo "pipe:${PIPESTATUS[0]}:${PIPESTATUS[1]}:${PIPESTATUS[@]}") && ` +
		`touch touched.txt && echo redirected > redirected.txt && printf 'teed\n' | tee teed.txt && ` +
		`wc notes.txt teed.txt && ` +
		`ps aux >/dev/null && env | grep -q '^PATH='; ` +
		`sleep 30 & pid=$!; ps aux | grep -q running && kill $pid; wait; echo "sig:$?"`
	gitengine.TestingEnableBundledBinary()
	s := newCommandProgramSession(t, prog, "")
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "bundled git", err)
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = s.dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.test", "GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitRun("init", "-q", "-b", "main")
	testutil.FailErr(t, "seed initial notes", os.WriteFile(filepath.Join(s.dir, "notes.txt"), []byte("first note\n"), 0o644))
	gitRun("add", "notes.txt")
	gitRun("commit", "-q", "-m", "seed commit")
	testutil.FailErr(t, "update notes", os.WriteFile(filepath.Join(s.dir, "notes.txt"), []byte("first note\nsecond note\n"), 0o644))
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	result := s.result(t)
	for _, want := range []string{
		`---`,
		`--- section ---`,
		`## main`,
		`seed commit`,
		`+second note`,
		"2\n1\n",
		`row:first note`,
		`row:second note`,
		`pipe:1:0:1 0`,
		`teed`,
		`total`,
		`sig:143`,
		`"completed_effects": 3`,
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("missing %q in result: %s", want, result)
		}
	}
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read messages", err)
	projected := messageview.TranscriptMessages(msgs)
	found := false
	for _, m := range projected {
		if m.Role == wire.MessageRoleTool && m.ToolResult != nil && m.ToolResult.Tool == "command" {
			found = true
			if m.Content == "" || m.ToolResult.CommandProgram == nil || m.ToolResult.CommandProgram.ID == "" || m.ToolResult.CommandProgram.State != "completed" {
				t.Fatalf("projected tool message missing content or command_program receipt: content=%q tr=%+v", m.Content, m.ToolResult)
			}
		}
	}
	if !found {
		t.Fatal("no projected command tool message")
	}

	for _, c := range []struct {
		name, program, code, cmd, word string
	}{
		{"sed unsupported command", "echo before > saved.txt; sed 'e' notes.txt", "COMMAND_OPTION_UNSUPPORTED", "sed", "e"},
		{"grep unsupported regex", "echo before > saved.txt; grep -P '(?R)' notes.txt", "GREP_REGEX_UNSUPPORTED", "grep", "(?R)"},
		{"kill pid", "echo before > saved.txt; kill 9999999", "COMMAND_JOB_UNKNOWN", "kill", "9999999"},
		{"redirect escape", "echo before > saved.txt; echo hi > /outside-root-escape.txt", "SURVEY_PATH_ESCAPE", "redirect >", "/outside-root-escape.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rs := newCommandProgramSession(t, c.program, "")
			testutil.FailErr(t, "prompt", rs.prompt(t.Context()))
			out := rs.result(t)
			// The refusal keeps the earlier redirect's effect and locates the stopped statement.
			for _, want := range []string{c.code, c.cmd, c.word, `"completed_effects": 1`, `"operation_source_line": 1`} {
				if !strings.Contains(out, want) {
					t.Fatalf("%s: missing %q in %s", c.name, want, out)
				}
			}
		})
	}
	t.Run("grep invalid regex utility error", func(t *testing.T) {
		rs := newCommandProgramSession(t, "echo before > saved.txt; grep '[' notes.txt; echo after:$?", "")
		testutil.FailErr(t, "prompt", rs.prompt(t.Context()))
		out := rs.result(t)
		for _, want := range []string{`after:2`, `grep: Unmatched [`, `"state": "completed"`, `"completed_effects": 1`} {
			if !strings.Contains(out, want) {
				t.Fatalf("grep invalid regex utility error: missing %q in %s", want, out)
			}
		}
	})
}

func TestCommandProgramEnvMatchesExternalChildEnv(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env binary not found")
	}
	t.Setenv("LYCAON_ENGINE_SECRET", "must-not-leak")
	t.Setenv("PW_CANARY_SECRET", "canary-secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("USER", "paintedwolf")
	t.Setenv("LANG", "en_US.UTF-8")

	prog := `export EXPORTED_KEY=exported_val; ` +
		`env | sort > native_env.txt; ` +
		`external -- ` + envBin + ` | sort > external_env.txt`
	s := newCommandProgramSession(t, prog, "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))

	nativeBytes, err := os.ReadFile(filepath.Join(s.dir, "native_env.txt"))
	testutil.FailErr(t, "read native_env.txt", err)
	externalBytes, err := os.ReadFile(filepath.Join(s.dir, "external_env.txt"))
	testutil.FailErr(t, "read external_env.txt", err)

	nativeText := string(nativeBytes)
	externalText := string(externalBytes)
	for _, secret := range []string{"LYCAON_ENGINE_SECRET", "PW_CANARY_SECRET", "OPENAI_API_KEY"} {
		if strings.Contains(nativeText, secret) || strings.Contains(externalText, secret) {
			t.Fatalf("leaked %s: native=%q external=%q", secret, nativeText, externalText)
		}
	}
	for _, required := range []string{"PATH=", "USER=paintedwolf", "LANG=en_US.UTF-8", "EXPORTED_KEY=exported_val"} {
		if !strings.Contains(nativeText, required) || !strings.Contains(externalText, required) {
			t.Fatalf("missing %s: native=%q external=%q", required, nativeText, externalText)
		}
	}

	sandboxKeys := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
		"GIT_SSH_COMMAND": true, "LYCAON_SOCKS_PROXY": true, "LYCAON_SSH_KNOWN_HOSTS": true,
	}
	filterSandbox := func(text string) []string {
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			if line == "" {
				continue
			}
			key, _, ok := strings.Cut(line, "=")
			if ok && sandboxKeys[key] {
				continue
			}
			out = append(out, line)
		}
		return out
	}
	if gotNative, gotExt := filterSandbox(nativeText), filterSandbox(externalText); !slices.Equal(gotNative, gotExt) {
		t.Fatalf("env | sort != external -- env | sort:\nnative:   %v\nexternal: %v", gotNative, gotExt)
	}
}

// Redirects and tee publish through the same write door, so both stop at the
// progress gate before touching the project and both publish once a checklist
// is recorded.
func TestCommandProgramTeeAndRedirectShareTheProgressGate(t *testing.T) {
	for _, c := range []struct{ name, program, target, want, stop string }{
		{"redirect", "echo via_redirect > redirected.txt", "redirected.txt", "via_redirect\n", `"stderr": ">: redirected.txt: `},
		{"tee", "printf 'via_tee\\n' | tee teed.txt", "teed.txt", "via_tee\n", `"stderr": "tee: `},
	} {
		t.Run(c.name+" without progress", func(t *testing.T) {
			s := newCommandProgramSessionWithoutProgress(t, c.program, "")
			state, err := s.h.Sessions.Manager.Coordinator.Guards.ProgramProgressState(t.Context(), s.session)
			testutil.FailErr(t, "progress state", err)
			if !progress.PrerequisiteMissing(state.Content, state.WorkerChild, state.ReviewLoopActive) {
				t.Fatal("expected root session to start without progress")
			}
			testutil.FailErr(t, "prompt", s.prompt(t.Context()))
			result := s.result(t)
			for _, want := range []string{`"stop_code": "PROGRESS_MISSING"`, `"completed_effects": 0`, c.stop, "[PROGRESS_MISSING]"} {
				if !strings.Contains(result, want) {
					t.Fatalf("missing %q in result: %s", want, result)
				}
			}
			var programState, stopCode string
			testutil.FailErr(t, "read program", s.h.DB.QueryRowContext(t.Context(), `SELECT p.state, p.stop_code FROM invocation_programs p
				JOIN invocation_receipts r ON r.id = p.parent_receipt_id WHERE r.session_id = ?`, s.session).Scan(&programState, &stopCode))
			if programState != "rejected" || stopCode != "PROGRESS_MISSING" {
				t.Fatalf("program state=%q stop=%q", programState, stopCode)
			}
			if _, err := os.Stat(filepath.Join(s.dir, c.target)); !os.IsNotExist(err) {
				t.Fatalf("%s was written without progress: %v", c.target, err)
			}
		})
		t.Run(c.name+" with progress", func(t *testing.T) {
			s := newCommandProgramSession(t, c.program, "")
			testutil.FailErr(t, "prompt", s.prompt(t.Context()))
			if result := s.result(t); strings.Contains(result, "PROGRESS_MISSING") || !strings.Contains(result, `"completed_effects": 1`) {
				t.Fatalf("%s did not publish: %s", c.name, result)
			}
			if got := s.programState(t); got != "completed" {
				t.Fatalf("program state = %q, want completed", got)
			}
			data, err := os.ReadFile(filepath.Join(s.dir, c.target))
			testutil.FailErr(t, "read written file", err)
			if string(data) != c.want {
				t.Fatalf("%s = %q, want %q", c.target, data, c.want)
			}
		})
	}
}

func TestCommandProgramKillSignalStatusAndJobReferences(t *testing.T) {
	prog := `sleep 30 & kill %1; wait; echo "pct1:$?"; ` +
		`sleep 30 & pid=$!; kill "$pid"; wait "$pid"; echo "bang:$?:$pid"`
	s := newCommandProgramSession(t, prog, "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	result := s.result(t)
	if !strings.Contains(result, "pct1:143\nbang:143:") || strings.Contains(result, "bang:143:\n") {
		t.Fatalf("unexpected kill/wait output: %q", result)
	}
}

func TestCommandProgramHTTPMessagesEndpointReturnsContentAndReceipt(t *testing.T) {
	s := newCommandProgramSession(t, `echo "http_messages_visible"`, "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+s.session+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
	w := httptest.NewRecorder()
	s.h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/sessions/%s/messages status = %d body = %s", s.session, w.Code, w.Body.String())
	}
	var page wire.SessionTranscriptPage
	testutil.FailErr(t, "decode transcript page", json.Unmarshal(w.Body.Bytes(), &page))
	found := false
	for _, m := range page.Messages {
		if m.Role == wire.MessageRoleTool && m.ToolResult != nil && m.ToolResult.Tool == "command" {
			found = true
			if !strings.Contains(m.Content, "http_messages_visible") || !strings.Contains(m.ToolResult.Content, "http_messages_visible") {
				t.Fatalf("HTTP message missing command output: content=%q tool_result.content=%q", m.Content, m.ToolResult.Content)
			}
			if m.ToolResult.CommandProgram == nil || m.ToolResult.CommandProgram.ID == "" || m.ToolResult.CommandProgram.State != "completed" {
				t.Fatalf("HTTP message missing command_program receipt: %+v", m.ToolResult)
			}
		}
	}
	if !found {
		t.Fatalf("no command tool message in HTTP response: %+v", page.Messages)
	}
}

func TestCommandProgramWcMultiFilePaddingMatchesGNU(t *testing.T) {
	s := newCommandProgramSession(t, `wc -l one.txt ten.txt && wc one.txt ten.txt`, "")
	testutil.FailErr(t, "seed one.txt", os.WriteFile(filepath.Join(s.dir, "one.txt"), []byte("a\n"), 0o644))
	testutil.FailErr(t, "seed ten.txt", os.WriteFile(filepath.Join(s.dir, "ten.txt"), []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"), 0o644))
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	result := s.result(t)
	want := " 1 one.txt\n10 ten.txt\n11 total\n 1  1  2 one.txt\n10 10 21 ten.txt\n11 11 23 total\n"
	if !strings.Contains(result, want) {
		t.Fatalf("wc output = %q, want substring %q", result, want)
	}
}
