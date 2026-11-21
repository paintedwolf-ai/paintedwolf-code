package curl

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCurlScreensManagedValuesAcrossCompressedTransferAndPublication(t *testing.T) {
	h := newHarness(t)
	h.tc.Identity.ProjectID = testdbseed.DefaultProjectID
	database := testdbfixture.Open(t, "jars.db")
	testdbseed.InsertSession(t, database, h.tc.Identity.SessionID, h.tc.Identity.ProjectID)
	values := credentialstore.NewEmpty(credentialstore.Slot{Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespaceManagedSecrets, Context: "curl transfer"}, func(string) bool { return true })
	service := secretcap.NewWithStore(database, values, nil)
	h.owners.HTTP.Secrets, h.owners.HTTP.Jars = service, service.Jars

	const cookie, token = "session-cookie-833", "session-token-189"
	delivered := make(chan bool, 2)
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		} else {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			delivered <- r.Header.Get("Cookie") == "sid="+cookie && r.Header.Get("X-Session") == token && string(body) == "session="+token
			w.Header().Set("X-Session", token)
		}
		z := gzip.NewWriter(w)
		_, _ = fmt.Fprintf(z, `{"session":%q,"confirmed":true}`, token)
		if err := z.Close(); err != nil {
			t.Error(err)
		}
	})
	withJars := func(args ...string) run {
		common := []string{"-s", "-v", "--compressed", "--managed-cookie-jar", "browser", "--managed-token-jar", "api"}
		return h.curl(t.Context(), append(common, args...)...)
	}
	h.tc.Identity.ToolCallID = "compressed-login"
	login := withJars("--capture-token", "session=json:session", "-D", "login.headers", server.URL+"/login")
	testutil.FailErr(t, "capture compressed response", login.err)
	h.tc.Identity.ToolCallID = "compressed-use"
	use := withJars("-H", "X-Session: {{token:session}}", "--data-raw", "session={{token:session}}", "-i", "-o", "reply.http", "-w", "%{http_code}", server.URL+"/use")
	testutil.FailErr(t, "publish screened response", use.err)
	if len(delivered) != 1 || !<-delivered || use.stdout != "200" {
		t.Fatal("issuer did not receive the scoped request and return success")
	}
	for _, r := range []run{login, use} {
		if !strings.Contains(r.evidence.EvidenceRef, "cookie_jar:") || !strings.Contains(r.evidence.EvidenceRef, "token_jar:") {
			t.Fatal("exchange lost managed jar evidence")
		}
	}
	reply := h.read("reply.http")
	if !strings.Contains(reply, `"confirmed":true`) || !strings.Contains(login.stdout, `"confirmed":true`) {
		t.Fatal("decoded public response fields did not survive screening")
	}
	for _, output := range []string{login.stdout, login.stderr, login.evidence.EvidenceRef, use.stdout, use.stderr, use.evidence.EvidenceRef, h.read("login.headers"), reply} {
		if strings.Contains(output, token) || strings.Contains(output, cookie) {
			t.Fatal("a managed value reached curl output")
		}
	}
}
