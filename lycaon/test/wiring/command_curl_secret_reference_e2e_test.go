package wiring

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testutil"
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
	run.script.steps = []citableStep{
		{command: "curl -s -H 'Authorization: Bearer " + reference + "' " + server.URL},
		{command: "curl -s '" + server.URL + "/?token=" + reference + "'"},
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
	run.script.steps = []citableStep{{command: "curl -s --retry 2 " + server.URL}}
	_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "call the service")
	testutil.FailErr(t, "prompt", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 1 || outcomes[0].stopCode == "" {
		run.logResults(t)
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("an unsupported form reached the service %d times", len(got))
	}
}

// Basic credentials carry a reference in their password, a placement curl
// declares, and the service receives the resolved value, with or without a
// declared service and a loopback capability.
func TestCommandCurlDeliversAReferenceInBasicCredentials(t *testing.T) {
	for _, declared := range []bool{false, true} {
		name := "plain"
		if declared {
			name = "declared service"
		}
		t.Run(name, func(t *testing.T) {
			run, sess := newSecretSinkRun(t)
			scope := secretcap.ScopeChat
			if declared {
				scope = secretcap.ScopeProject
			}
			reference := run.generatedReference(t, sess, scope)
			server, seen := requestRecorder(t, run)
			command := "curl -sS -u 'user:" + reference + "' " + server.URL
			step := citableStep{command: command}
			if declared {
				parsed, err := url.Parse(server.URL)
				testutil.FailErr(t, "parse server", err)
				port, err := strconv.Atoi(parsed.Port())
				testutil.FailErr(t, "parse port", err)
				step = citableStep{tool: "command", args: map[string]any{
					"command": command, "secret_use": map[string]any{"services": []any{server.URL}},
					"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{port}}},
				}}
			}
			run.script.steps = []citableStep{step}
			_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "call the service")
			testutil.FailErr(t, "prompt", err)
			outcomes := run.outcomes(t)
			if len(outcomes) != 1 || outcomes[0].state != "completed" || outcomes[0].stopCode != "" {
				run.logResults(t)
				t.Fatalf("outcomes = %+v", outcomes)
			}
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+sinkSentinel))
			if got := seen(); len(got) != 1 || got[0] != want {
				run.logResults(t)
				t.Fatalf("service saw %q", got)
			}
		})
	}
}
