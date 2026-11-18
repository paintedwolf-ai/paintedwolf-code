package wiring

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandCurlUsesManagedJarsAcrossSubmissions(t *testing.T) {
	run, _ := newSecretSinkRun(t)
	const cookie = "fixture-session-cookie-871"
	const number = "9876543210123456789"
	const replacement = "uncaptured-session-value-974"
	seen := make(chan [3]string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.Header().Set("X-Session", sinkSentinel)
			_, _ = io.WriteString(w, `{"result":{"id":`+number+`}}`)
			return
		}
		if r.URL.Path == "/refresh" {
			w.Header().Set("X-Session", replacement)
			_, _ = io.WriteString(w, `{"status":"missing required capture"}`)
			return
		}
		seen <- [3]string{r.Header.Get("Cookie"), r.Header.Get("X-Session"), r.Header.Get("X-Number")}
		_, _ = io.WriteString(w, "accepted "+cookie+" "+sinkSentinel+" "+number)
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse fixture URL", err)
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	testutil.FailErr(t, "parse fixture port", err)
	if !run.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(run.session, []uint16{uint16(port)}, "managed-jar", "fixture", nil) {
		t.Fatal("loopback grant failed")
	}
	const jars = "curl -s --managed-cookie-jar browser --managed-token-jar api "
	run.seed(t, "refresh.txt", "previous publication")
	run.script.steps = []citableStep{
		{command: jars + "--capture-token session=header:X-Session --capture-token number=json:result.id -D login.headers " + server.URL + "/login"},
		{command: jars + "--capture-token session=header:X-Session --capture-token number=json:result.id -o refresh.txt " + server.URL + "/refresh"},
		{command: jars + "-H 'X-Session: {{token:session}}' -H 'X-Number: {{token:number}}' -o response.txt " + server.URL + "/check"},
	}
	_, err = run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "use the fixture service")
	testutil.FailErr(t, "submit command sequence", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 3 || outcomes[0].stopCode != "" || outcomes[1].stopCode != "HTTP_REQUEST_TOKEN_CAPTURE_FAILED" || outcomes[2].stopCode != "" {
		run.logResults(t)
		t.Fatalf("program outcomes = %+v", outcomes)
	}
	select {
	case got := <-seen:
		if got != [3]string{"sid=" + cookie, sinkSentinel, number} {
			t.Fatal("service did not receive the scoped values")
		}
	default:
		t.Fatal("service did not receive the second request")
	}
	if got := run.file(t, filepath.Join(run.dir, "refresh.txt")); got != "previous publication" {
		t.Fatal("incomplete capture replaced a published file")
	}
	messages, err := run.h.Store.Transcript.GetMessages(t.Context(), run.session)
	testutil.FailErr(t, "read transcript", err)
	for _, message := range messages {
		for _, value := range []string{replacement, sinkSentinel, cookie, number} {
			if strings.Contains(message.Content, value) {
				t.Fatal("a captured or withheld value reached the transcript")
			}
		}
	}
	for _, file := range []string{"login.headers", "response.txt"} {
		b, err := os.ReadFile(filepath.Join(run.dir, file))
		testutil.FailErr(t, "read published output", err)
		if len(b) == 0 || strings.Contains(string(b), cookie) || strings.Contains(string(b), sinkSentinel) || strings.Contains(string(b), number) {
			t.Fatalf("unsafe or missing bytes in %s", file)
		}
	}
}
