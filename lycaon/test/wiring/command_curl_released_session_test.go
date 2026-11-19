package wiring

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandCurlContinuesReleasedSessionWithoutStandaloneHTTP(t *testing.T) {
	run, session := newSecretSinkRun(t)
	for _, tool := range run.h.ToolRegistry.List() {
		if tool.Name == "http_request" {
			t.Fatal("standalone HTTP is registered in the production host")
		}
	}

	type delivery struct{ path, cookie, body string }
	seen := make(chan delivery, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
		}
		seen <- delivery{req.URL.Path, req.Header.Get("Cookie"), string(body)}
		_, _ = fmt.Fprint(w, "confirmed ", sinkSentinel)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse fixture URL", err)
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	testutil.FailErr(t, "parse fixture port", err)
	if !run.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(run.session, []uint16{uint16(port)}, "released-session", "fixture", nil) {
		t.Fatal("loopback grant failed")
	}

	jars := run.h.Server.Admin.Capabilities.CheckpointActions.ManagedSecrets
	put := func(name, origin, purpose, value string) string {
		t.Helper()
		stored, err := jars.Put(t.Context(), secretcap.PutRequest{
			ProjectID: session.ProjectID, ChatSessionID: run.session, SessionID: run.session,
			OperationID: "released-" + name, Scope: secretcap.ScopeChat,
			Name: name, Origin: origin, Purpose: purpose, Value: value,
		})
		testutil.FailErr(t, "seed released jar", err)
		return stored.Metadata.Reference
	}
	issuer, label := secretmatch.HTTPDestination(target)
	tokenRef := put("api", secretcap.OriginTokenJar, "HTTP token jar", fmt.Sprintf(`{"session":{"value":%q,"origin":%q,"origin_label":%q}}`, sinkSentinel, issuer, label))
	const cookie = "released-cookie-session-461"
	cookieRef := put("browser", secretcap.OriginCookieJar, "HTTP cookie jar", fmt.Sprintf(`{"cookies":[{"name":"sid","value":%q,"domain":%q,"host_only":true,"path":"/private","http_only":true,"created":"2026-01-01T00:00:00Z"}]}`, cookie, target.Hostname()))
	const command = "curl -s --managed-cookie-jar browser --managed-token-jar api "
	const marker = "{{token:session}}"
	run.seed(t, "body.txt", marker)
	run.script.steps = []citableStep{
		{command: command + "--data-raw '" + marker + "' " + server.URL + "/private/inline"},
		{command: command + "--data-binary @body.txt " + server.URL + "/public/file"},
		{command: command + "--data-binary @- " + server.URL + "/private/stdin < body.txt"},
		{command: command + "--data-raw '{{token:missing}}' " + server.URL + "/private/refused"},
	}
	_, err = run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "continue the saved service session")
	testutil.FailErr(t, "submit saved session", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 4 || outcomes[0].stopCode != "" || outcomes[1].stopCode != "" || outcomes[2].stopCode != "" || outcomes[3].stopCode != "HTTP_REQUEST_TOKEN_NOT_HELD" {
		run.logResults(t)
		t.Fatalf("command outcomes = %+v", outcomes)
	}
	want := []delivery{{"/private/inline", "sid=" + cookie, sinkSentinel}, {"/public/file", "", marker}, {"/private/stdin", "sid=" + cookie, marker}}
	if len(seen) != len(want) {
		t.Fatalf("service received %d requests, want %d", len(seen), len(want))
	}
	for _, expected := range want {
		if got := <-seen; got != expected {
			t.Errorf("wrong scoped cookie or body at %s", got.path)
		}
	}
	rows, err := run.h.DB.QueryContext(t.Context(), `SELECT evidence_ref FROM invocation_program_executions WHERE native_command = 'curl' AND evidence_kind = 'http_exchange'`)
	testutil.FailErr(t, "load exchange receipts", err)
	defer rows.Close()
	var continued int
	for rows.Next() {
		var refs string
		testutil.FailErr(t, "read jar references", rows.Scan(&refs))
		if strings.Contains(refs, sinkSentinel) || strings.Contains(refs, cookie) {
			t.Fatal("managed value reached the durable exchange evidence")
		}
		if strings.Contains(refs, "cookie_jar:"+cookieRef) && strings.Contains(refs, "token_jar:"+tokenRef) {
			continued++
		}
	}
	testutil.FailErr(t, "read exchange receipts", rows.Err())
	if continued != len(want) {
		t.Fatalf("%d exchanges retained both released references, want %d", continued, len(want))
	}
}
