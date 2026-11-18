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
	seen := make(chan [2]string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.Header().Set("X-Session", sinkSentinel)
			_, _ = io.WriteString(w, "login complete")
			return
		}
		seen <- [2]string{r.Header.Get("Cookie"), r.Header.Get("X-Session")}
		_, _ = io.WriteString(w, "accepted "+cookie+" "+sinkSentinel)
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
	run.script.steps = []citableStep{
		{command: jars + "--capture-token session=header:X-Session -D login.headers " + server.URL + "/login"},
		{command: jars + "-H 'X-Session: {{token:session}}' -o response.txt " + server.URL + "/check"},
	}
	_, err = run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "use the fixture service")
	testutil.FailErr(t, "submit command sequence", err)
	outcomes := run.outcomes(t)
	if len(outcomes) != 2 || outcomes[0].stopCode != "" || outcomes[1].stopCode != "" {
		run.logResults(t)
		t.Fatalf("program outcomes = %+v", outcomes)
	}
	select {
	case got := <-seen:
		if got != [2]string{"sid=" + cookie, sinkSentinel} {
			t.Fatal("service did not receive both scoped values")
		}
	default:
		t.Fatal("service did not receive the second request")
	}
	for _, file := range []string{"login.headers", "response.txt"} {
		b, err := os.ReadFile(filepath.Join(run.dir, file))
		testutil.FailErr(t, "read published output", err)
		if len(b) == 0 || strings.Contains(string(b), cookie) || strings.Contains(string(b), sinkSentinel) {
			t.Fatalf("unsafe or missing bytes in %s", file)
		}
	}
}
