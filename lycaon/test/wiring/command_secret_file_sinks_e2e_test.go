package wiring

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const sinkSentinel = "sink-sentinel-Vq7rT2mZ9xK4pL8w"

type secretSinkRun struct {
	h       *Harness
	session string
	dir     string
	script  *citableScriptLLM
}

func newSecretSinkRun(t *testing.T) (*secretSinkRun, *wire.Session) {
	t.Helper()
	dir := t.TempDir()
	script := &citableScriptLLM{}
	h := BuildForTest(t, WithLLMClient(script))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	h.SeedProgress(t, t.Context(), sess.ID)
	return &secretSinkRun{h: h, session: sess.ID, dir: dir, script: script}, sess
}

// suppliedReference stores the sentinel the way a person enters it in settings.
func (r *secretSinkRun) suppliedReference(t *testing.T, sess *wire.Session) string {
	t.Helper()
	body, err := json.Marshal(wire.CreateManagedSecretRequest{OperationID: uuid.NewString(), Name: "sink token", Purpose: "file sink test", SecretValue: sinkSentinel})
	testutil.FailErr(t, "encode secret", err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects/"+sess.ProjectID+"/secrets", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+api.TestAPIToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.h.Server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create secret = %d %s", response.Code, response.Body)
	}
	var created wire.ManagedSecret
	testutil.FailErr(t, "decode secret", json.Unmarshal(response.Body.Bytes(), &created))
	return created.Reference
}

// detectedReference stores the sentinel as a tracked chat value no person holds.
func (r *secretSinkRun) detectedReference(t *testing.T, sess *wire.Session) string {
	t.Helper()
	put, err := r.h.Server.Admin.Capabilities.CheckpointActions.ManagedSecrets.Put(t.Context(), secretcap.PutRequest{
		ProjectID: sess.ProjectID, ChatSessionID: r.session, SessionID: r.session, OperationID: uuid.NewString(),
		Name: "sink token", Scope: secretcap.ScopeChat, Origin: secretcap.OriginDetected, Value: sinkSentinel,
	})
	testutil.FailErr(t, "put secret", err)
	return put.Metadata.Reference
}

// generatedReference asks the host for chat-scoped random material.
func (r *secretSinkRun) generatedReference(t *testing.T, sess *wire.Session) string {
	t.Helper()
	out, err := r.h.ToolRegistry.Run(t.Context(), "secret_generate", map[string]any{"name": "sink token", "scope": "chat"}, tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: sess.ProjectID, SessionID: r.session, ParentSessionID: r.session, ToolCallID: "generate-sink-token"}})
	testutil.FailErr(t, "generate secret", err)
	var created struct {
		Secret struct {
			Reference string `json:"reference"`
		} `json:"secret"`
	}
	testutil.FailErr(t, "decode generated secret", json.Unmarshal([]byte(out), &created))
	return created.Secret.Reference
}

func (r *secretSinkRun) seed(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(r.dir, name)
	testutil.FailErr(t, "seed "+name, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func (r *secretSinkRun) file(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+path, err)
	return string(body)
}

func (r *secretSinkRun) logResults(t *testing.T) {
	t.Helper()
	messages, err := r.h.Store.GetMessages(t.Context(), r.session)
	testutil.FailErr(t, "messages", err)
	for _, message := range messages {
		if message.Role == wire.MessageRoleTool {
			t.Log(message.Content)
		}
	}
}

type programOutcome struct{ state, stopCode string }

// outcomes reads each command's typed receipt in execution order and fails if
// the supplied value reached the transcript in any form.
func (r *secretSinkRun) outcomes(t *testing.T) []programOutcome {
	t.Helper()
	messages, err := r.h.Store.GetMessages(t.Context(), r.session)
	testutil.FailErr(t, "messages", err)
	for _, message := range messages {
		if strings.Contains(message.Content, sinkSentinel) {
			t.Fatalf("supplied value reached the %s transcript", message.Role)
		}
	}
	rows, err := r.h.DB.QueryContext(t.Context(), `SELECT p.state, p.stop_code FROM invocation_programs p JOIN invocation_receipts r ON r.id = p.parent_receipt_id WHERE r.session_id = ? ORDER BY p.rowid`, r.session)
	testutil.FailErr(t, "query programs", err)
	defer rows.Close()
	var outcomes []programOutcome
	for rows.Next() {
		var outcome programOutcome
		testutil.FailErr(t, "scan program", rows.Scan(&outcome.state, &outcome.stopCode))
		outcomes = append(outcomes, outcome)
	}
	testutil.FailErr(t, "read programs", rows.Err())
	return outcomes
}

// A host-generated chat value reaches a local file without a card, through both
// the edit and replace-lines sinks.
func TestCommandFileSinksResolveAChatGeneratedReference(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.generatedReference(t, sess)
	edited := run.seed(t, "edit.env", "TOKEN=old\nkeep\n")
	lines := run.seed(t, "lines.env", "first\nkeep\n")
	run.script.steps = []citableStep{
		{command: "edit --path edit.env --old-string TOKEN=old --new-string 'TOKEN=" + reference + "'"},
		{command: "replace-lines --path lines.env --start 1 --end 1 --content 'TOKEN=" + reference + "'"},
		{command: "edit --path edit.env --old-string 'TOKEN=" + reference + "' --new-string TOKEN=rotated"},
	}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "write the token")
	testutil.FailErr(t, "prompt", err)
	for i, outcome := range run.outcomes(t) {
		if outcome.state != "completed" {
			run.logResults(t)
			t.Fatalf("command %d = %+v", i, outcome)
		}
	}
	got := run.file(t, lines)
	value, ok := strings.CutPrefix(got, "TOKEN=")
	if !ok || !strings.HasSuffix(value, "\nkeep\n") || strings.Contains(got, reference) || len(value) <= len("\nkeep\n") {
		t.Fatalf("replace-lines did not write the resolved value: file holds %d bytes", len(got))
	}
	// The old-string sink resolved too: the third call matched the written value.
	if got := run.file(t, edited); got != "TOKEN=rotated\nkeep\n" {
		t.Fatalf("edit left %d bytes, want the rotated line", len(got))
	}
}

// A value a person holds leaves only while their chat is unlocked, so a host that
// cannot verify presence stops at the file screen with the file untouched.
func TestCommandFileSinkKeepsAHeldValueWithoutPresence(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.suppliedReference(t, sess)
	target := run.seed(t, "config.env", "TOKEN=old\n")
	run.script.steps = []citableStep{{command: "edit --path config.env --old-string TOKEN=old --new-string 'TOKEN=" + reference + "'"}}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "write the token")
	testutil.FailErr(t, "prompt", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 1 || outcomes[0].stopCode != "OUTBOUND_SECRET_SCREEN_FAILED" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if got := run.file(t, target); got != "TOKEN=old\n" {
		t.Fatalf("file = %q", got)
	}
}

// A value the host did not generate for this chat is not released by policy: the
// file screen raises a card naming the reached write target, and only approval
// lets bytes land.
func TestCommandFileSinkAsksBeforeWritingATrackedSecret(t *testing.T) {
	for _, approved := range []bool{false, true} {
		name := "declined"
		if approved {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) {
			run, sess := newSecretSinkRun(t)
			reference := run.detectedReference(t, sess)
			target := run.seed(t, "config.env", "TOKEN=old\n")
			run.script.steps = []citableStep{{command: "edit --path config.env --old-string TOKEN=old --new-string 'TOKEN=" + reference + "'"}}
			done := make(chan error, 1)
			go func() {
				_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "write the token")
				done <- err
			}()
			kind := wire.CheckpointKindToolApproval
			var card wire.CheckpointEvent
			testutil.WaitFor(t, func() bool {
				select {
				case err := <-done:
					testutil.FailErr(t, "prompt", err)
					run.logResults(t)
					t.Fatal("the turn ended without a card")
				default:
				}
				pending, err := run.h.Sessions.Checkpoints.ListPending(t.Context(), run.session, &kind)
				if err != nil || len(pending) == 0 {
					return false
				}
				card = pending[0]
				return true
			})
			if run.file(t, target) != "TOKEN=old\n" {
				t.Fatal("bytes landed before the decision")
			}
			encoded, err := json.Marshal(card)
			testutil.FailErr(t, "encode card", err)
			if strings.Contains(string(encoded), sinkSentinel) {
				t.Fatal("card carries the value")
			}
			if !strings.Contains(string(encoded), target) && !strings.Contains(string(encoded), "config.env") {
				t.Fatalf("card does not name the write target: %s", encoded)
			}
			_, err = run.h.Sessions.Checkpoints.ResolveCheckpoint(run.h.OwnerCtx(t, t.Context()), run.session, card.ID, kind, &hitl.DecisionResult{Approved: approved}, nil)
			testutil.FailErr(t, "resolve card", err)
			testutil.FailErr(t, "prompt", <-done)
			want := "TOKEN=old\n"
			if approved {
				want = "TOKEN=" + sinkSentinel + "\n"
			}
			if got := run.file(t, target); got != want {
				t.Fatalf("file = %q, want %q", got, want)
			}
			run.outcomes(t)
		})
	}
}

// A builtin's byte stream and a path operand are not declared sinks: each form
// refuses before anything is written, whatever would have consumed the bytes.
func TestCommandSecretReferenceOutsideDeclaredSinksIsRefused(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.suppliedReference(t, sess)
	existing := run.seed(t, "existing.env", "keep\n")
	commands := []string{
		"printf 'TOKEN=" + reference + "\\n' > redirect.env",
		"printf 'TOKEN=" + reference + "\\n' | tee piped.env",
		"cat > heredoc.env <<'EOF'\nTOKEN=" + reference + "\nEOF",
		"edit --path '" + reference + "' --old-string keep --new-string changed",
	}
	for _, command := range commands {
		run.script.steps = append(run.script.steps, citableStep{command: command})
	}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "write the token")
	testutil.FailErr(t, "prompt", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != len(commands) {
		t.Fatalf("outcomes = %d, want %d", len(outcomes), len(commands))
	}
	for i, outcome := range outcomes {
		if outcome.stopCode != "TOOL_ARGS_INVALID" {
			t.Errorf("%q = %+v", commands[i], outcome)
		}
	}
	for _, name := range []string{"redirect.env", "piped.env", "heredoc.env"} {
		if _, err := os.Stat(filepath.Join(run.dir, name)); err == nil {
			body, _ := os.ReadFile(filepath.Join(run.dir, name))
			if strings.Contains(string(body), sinkSentinel) {
				t.Errorf("%s holds the value", name)
			}
		}
	}
	if got := run.file(t, existing); got != "keep\n" {
		t.Fatalf("existing file = %q", got)
	}
}
