package conformance_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/commandconformance"
)

var managedHTTPContract = contractCase{
	Case: commandconformance.Case{
		ID: "http-curl-managed-session", Commands: []string{"curl"},
		Forms: []string{"curl:--managed-cookie-jar", "curl:--managed-token-jar", "curl:--capture-token", "curl:--review-redaction"},
	},
	Prepare: prepareManagedHTTPSession,
	Stdout:  "session started\nsession resumed\n",
}

func prepareManagedHTTPSession(t *testing.T, c commandconformance.Case) commandconformance.Case {
	t.Helper()
	const token = "contract-session-token-928"
	const cookie = "contract-session-cookie-653"
	paths := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		switch r.URL.Path {
		case "/start":
			if r.Header.Get("Cookie") != "" || r.Header.Get("X-Session") != "" {
				t.Error("a new jar sent credentials")
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: cookie, Path: "/resume", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.Header().Set("X-Session", token)
			_, _ = fmt.Fprintln(w, "session started")
		case "/resume":
			if r.Header.Get("Cookie") != "sid="+cookie || r.Header.Get("X-Session") != token {
				t.Error("the resumed request lost its scoped session")
			}
			_, _ = fmt.Fprintln(w, "session resumed")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(func() {
		server.Close()
		if len(paths) != 2 || <-paths != "/start" || <-paths != "/resume" {
			t.Error("the service did not receive exactly one start and one resume")
		}
	})
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse fixture URL", err)
	common := "curl -s --managed-cookie-jar browser --managed-token-jar api"
	c.Program = common + " --capture-token session=header:X-Session " + server.URL + "/start && " +
		common + " --review-redaction stale -H 'X-Session: {{token:session}}' " + server.URL + "/resume"
	c.CapabilityRequest = fmt.Sprintf(`{"loopback_connect":{"ports":[%s]}}`, target.Port())
	return c
}
