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

// generatedReference stores the sentinel as host-generated material in a scope.
func (r *secretSinkRun) generatedReference(t *testing.T, sess *wire.Session, scope string) string {
	t.Helper()
	put, err := r.h.Server.Admin.Capabilities.CheckpointActions.ManagedSecrets.Put(t.Context(), secretcap.PutRequest{
		ProjectID: sess.ProjectID, ChatSessionID: r.session, SessionID: r.session, OperationID: uuid.NewString(),
		Name: "sink token", Purpose: "file sink test", Scope: scope, Origin: secretcap.OriginGenerated, Value: sinkSentinel,
		Format: secretcap.FormatAlphanumeric, EntropyBits: 128,
	})
	testutil.FailErr(t, "put secret", err)
	return put.Metadata.Reference
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
// the edit and replace-lines sinks, and never reaches the transcript.
func TestCommandFileSinksResolveAChatGeneratedReference(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.generatedReference(t, sess, secretcap.ScopeChat)
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
	if got := run.file(t, lines); got != "TOKEN="+sinkSentinel+"\nkeep\n" {
		t.Fatalf("replace-lines left %d bytes, want the resolved line", len(got))
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
			reference := run.generatedReference(t, sess, secretcap.ScopeProject)
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
			owner := run.h.OwnerCtx(t, t.Context())
			if approved {
				_, err = run.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(owner, run.session, card.ID, card.ToolApproval.Plan.RecommendedOptionID)
			} else {
				_, err = run.h.Sessions.Checkpoints.ResolveCheckpoint(owner, run.session, card.ID, kind, &hitl.DecisionResult{Approved: false}, nil)
			}
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

// A builtin's argv, a path operand and a document body are not declared sinks:
// each form refuses before anything is written.
func TestCommandSecretReferenceOutsideDeclaredSinksIsRefused(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.suppliedReference(t, sess)
	existing := run.seed(t, "existing.env", "keep\n")
	commands := []string{
		"printf 'TOKEN=" + reference + "\\n' > redirect.env",
		"printf 'TOKEN=" + reference + "\\n' | tee piped.env",
		"cat > heredoc.env <<'EOF'\nTOKEN=" + reference + "\nEOF",
		"cat > herestring.env <<< 'TOKEN=" + reference + "'",
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
		if outcome.stopCode != "SECRET_REFERENCE_OUTSIDE_SINK" {
			t.Errorf("%q = %+v", commands[i], outcome)
		}
	}
	for _, name := range []string{"redirect.env", "piped.env", "heredoc.env", "herestring.env"} {
		if body, err := os.ReadFile(filepath.Join(run.dir, name)); err == nil && len(body) > 0 {
			t.Errorf("%s holds %d bytes", name, len(body))
		}
	}
	if got := run.file(t, existing); got != "keep\n" {
		t.Fatalf("existing file = %q", got)
	}
}

// Resolution belongs to one invocation: a later edit in the same program gets its
// own plain value, not the earlier operation's resolved copy.
func TestCommandSecretResolutionStaysWithItsOperation(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.generatedReference(t, sess, secretcap.ScopeChat)
	first := run.seed(t, "first.env", "TOKEN=old\n")
	second := run.seed(t, "second.env", "TOKEN=old\n")
	run.script.steps = []citableStep{{command: "edit --path first.env --old-string TOKEN=old --new-string 'TOKEN=" + reference + "' && edit --path second.env --old-string TOKEN=old --new-string TOKEN=plain"}}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "write both")
	testutil.FailErr(t, "prompt", err)
	run.outcomes(t)
	if got := run.file(t, first); got != "TOKEN="+sinkSentinel+"\n" {
		t.Fatalf("first file left %d bytes, want the resolved line", len(got))
	}
	if got := run.file(t, second); got != "TOKEN=plain\n" {
		t.Fatalf("second file left %d bytes, want the plain line", len(got))
	}
}

// A value line-oriented sinks must refuse reaches a document through jq-edit, which
// encodes it for the document's format.
func TestCommandJqEditArgCarriesAValueLineSinksRefuse(t *testing.T) {
	const multiline = sinkSentinel + "\nsecond line"
	run, sess := newSecretSinkRun(t)
	put, err := run.h.Server.Admin.Capabilities.CheckpointActions.ManagedSecrets.Put(t.Context(), secretcap.PutRequest{
		ProjectID: sess.ProjectID, ChatSessionID: run.session, SessionID: run.session, OperationID: uuid.NewString(),
		Name: "multiline token", Scope: secretcap.ScopeChat, Origin: secretcap.OriginGenerated, Value: multiline,
		Format: secretcap.FormatAlphanumeric, EntropyBits: 128,
	})
	testutil.FailErr(t, "put secret", err)
	reference := put.Metadata.Reference
	lines := run.seed(t, "lines.env", "TOKEN=old\n")
	document := run.seed(t, "settings.json", "{\"name\":\"svc\"}\n")
	run.script.steps = []citableStep{
		{command: "edit --path lines.env --old-string TOKEN=old --new-string 'TOKEN=" + reference + "'"},
		{command: "jq-edit --path settings.json --query '.token = $token' --arg token '" + reference + "'"},
		{command: "jq-edit --path settings.json --query '.name = $name' --arg name renamed"},
		{command: "jq-edit --path '" + reference + "' --query . --arg token value"},
		{command: "jq-edit --path settings.json --query '.x = \"" + reference + "\"'"},
	}
	_, err = run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "write the token")
	testutil.FailErr(t, "prompt", err)
	outcomes := run.outcomes(t)
	want := []string{"SECRET_REFERENCE_VALUE_UNSAFE", "", "", "SECRET_REFERENCE_OUTSIDE_SINK", "SECRET_REFERENCE_OUTSIDE_SINK"}
	if len(outcomes) != len(want) {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	for i, code := range want {
		if outcomes[i].stopCode != code {
			t.Errorf("command %d = %+v, want %q", i, outcomes[i], code)
		}
	}
	if got := run.file(t, lines); got != "TOKEN=old\n" {
		t.Fatalf("line sink left %d bytes", len(got))
	}
	var written map[string]string
	testutil.FailErr(t, "decode document", json.Unmarshal([]byte(run.file(t, document)), &written))
	if written["token"] != multiline || written["name"] != "renamed" || len(written) != 2 {
		t.Fatalf("document has %d keys; token match %t", len(written), written["token"] == multiline)
	}
}
