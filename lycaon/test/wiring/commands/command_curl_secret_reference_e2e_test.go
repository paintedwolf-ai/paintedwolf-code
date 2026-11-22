package commands

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/test/wiring"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// requestRecorder is a loopback service the session holds a port grant for;
// it records each request's Authorization header.
func requestRecorder(t *testing.T, run *secretSinkRun) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.WriteString(w, "ok\n")
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse server", err)
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	testutil.FailErr(t, "parse port", err)
	if !run.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(run.session, []uint16{uint16(port)}, "curl-recorder", "test-listener", nil) {
		t.Fatal("loopback grant failed")
	}
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// Native curl delivers a chat value in a request header and refuses one in its
// URL, which is not a request placement curl declares.
func TestCommandCurlDeliversAReferenceOnlyInARequestPlacement(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.generatedReference(t, sess, secretcap.ScopeChat)
	server, seen := requestRecorder(t, run)
	run.script.Steps = []wiring.CitableStep{
		{Command: "curl -s -H 'Authorization: Bearer " + reference + "' " + server.URL},
		{Command: "curl -s '" + server.URL + "/?token=" + reference + "'"},
	}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "call the service")
	testutil.FailErr(t, "prompt", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 2 || outcomes[0].stopCode != "" || outcomes[1].stopCode != "SECRET_REFERENCE_OUTSIDE_SINK" {
		run.logResults(t)
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if got := seen(); len(got) != 1 || got[0] != "Bearer "+sinkSentinel {
		t.Fatalf("service saw %q", got)
	}
}

// An unsupported curl form refuses instead of running an installed curl
// outside the http_request owner.
func TestCommandCurlUnsupportedFormDoesNotLaunchInstalledCurl(t *testing.T) {
	run, _ := newSecretSinkRun(t)
	server, seen := requestRecorder(t, run)
	run.script.Steps = []wiring.CitableStep{{Command: "curl -s --retry 2 " + server.URL}}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "call the service")
	testutil.FailErr(t, "prompt", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 1 || outcomes[0].stopCode != "COMMAND_OPTION_UNSUPPORTED" {
		run.logResults(t)
		t.Fatalf("outcomes = %+v, want one COMMAND_OPTION_UNSUPPORTED refusal", outcomes)
	}
	var gaps int
	testutil.FailErr(t, "count native gap launches", run.h.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions e
		JOIN invocation_programs p ON p.id = e.program_id JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? AND e.native_gap_json != ''`, run.session).Scan(&gaps))
	if gaps != 0 {
		t.Fatalf("an unsupported curl form launched the installed program %d times", gaps)
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("an unsupported form reached the service %d times", len(got))
	}
}

// Basic credentials carry a chat value in their password, a placement curl
// declares, and the service receives the resolved value.
func TestCommandCurlDeliversAReferenceInBasicCredentials(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.generatedReference(t, sess, secretcap.ScopeChat)
	server, seen := requestRecorder(t, run)
	run.script.Steps = []wiring.CitableStep{{Command: "curl -sS -u 'user:" + reference + "' " + server.URL}}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "call the service")
	testutil.FailErr(t, "prompt", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 1 || outcomes[0].state != "completed" || outcomes[0].stopCode != "" {
		run.logResults(t)
		t.Fatalf("outcomes = %+v", outcomes)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+sinkSentinel))
	if got := seen(); len(got) != 1 || got[0] != want {
		t.Fatalf("service saw %q", got)
	}
}

// A person who allows a project value for a declared service in this chat,
// while setting the service up, has curl send it there in basic credentials
// without being asked again.
func TestCommandCurlUsesTheChatApprovalForItsDeclaredService(t *testing.T) {
	run, sess := newSecretSinkRun(t)
	reference := run.generatedReference(t, sess, secretcap.ScopeProject)
	server, seen := requestRecorder(t, run)
	parsed, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse server", err)
	port, err := strconv.Atoi(parsed.Port())
	testutil.FailErr(t, "parse port", err)
	declared := func(command string) wiring.CitableStep {
		return wiring.CitableStep{Tool: "command", Args: map[string]any{
			"command": command, "secret_use": map[string]any{"services": []any{server.URL}},
			"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{port}}},
		}}
	}
	run.script.Steps = []wiring.CitableStep{
		declared("SERVICE_PASSWORD='" + reference + "' /bin/true"),
		declared("curl -sS -u 'user:" + reference + "' " + server.URL),
	}
	done := make(chan error, 1)
	go func() {
		_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "set up and call the service")
		done <- err
	}()
	owner := run.h.OwnerCtx(t, t.Context())
	kind := wire.CheckpointKindToolApproval
	var answered []string
	for finished := false; !finished; {
		testutil.WaitFor(t, "a card or the end of the turn", func() bool {
			select {
			case err := <-done:
				testutil.FailErr(t, "prompt", err)
				finished = true
				return true
			default:
			}
			pending, err := run.h.Sessions.Checkpoints.ListPending(t.Context(), run.session, &kind)
			if err != nil || len(pending) == 0 {
				return false
			}
			card := pending[0]
			answered = append(answered, card.ToolApproval.ToolCallID+" "+string(card.ToolApproval.Plan.Stage))
			_, err = run.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(owner, run.session, card.ID, card.ToolApproval.Plan.RecommendedOptionID)
			testutil.FailErr(t, "approve card", err)
			return true
		})
	}
	outcomes := run.outcomes(t)
	if len(outcomes) != 2 || outcomes[1].state != "completed" || outcomes[1].stopCode != "" {
		run.logResults(t)
		t.Fatalf("outcomes = %+v; answered %q", outcomes, answered)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+sinkSentinel))
	if got := seen(); len(got) != 1 || got[0] != want {
		t.Fatalf("service saw %q; answered %q", got, answered)
	}
}
