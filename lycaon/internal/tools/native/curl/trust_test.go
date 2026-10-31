package curl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/tlstest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// tlsFixture is a hermetic HTTPS server whose certificate a test CA signs.
type tlsFixture struct {
	server *httptest.Server
	ca     *tlstest.CA
	port   string
	hits   atomic.Int32
	mu     sync.Mutex
	sni    []string
	peers  []string
}

func newTLSFixture(t *testing.T, clientCAs *tlstest.CA, handler http.HandlerFunc) *tlsFixture {
	t.Helper()
	f := &tlsFixture{ca: tlstest.NewCA(t, "Painted Wolf Test CA")}
	leaf := f.ca.Server(t, "app.example.test", "127.0.0.1")
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			f.peers = append(f.peers, r.TLS.PeerCertificates[0].Subject.CommonName)
		}
		f.mu.Unlock()
		handler(w, r)
	}))
	f.server.TLS = &tls.Config{Certificates: []tls.Certificate{leaf.TLS}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		f.mu.Lock()
		f.sni = append(f.sni, hello.ServerName)
		f.mu.Unlock()
		return nil, nil
	}}
	if clientCAs != nil {
		f.server.TLS.ClientAuth, f.server.TLS.ClientCAs = tls.RequireAndVerifyClientCert, clientCAs.Pool()
	}
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	f.port = f.server.URL[strings.LastIndexByte(f.server.URL, ':')+1:]
	return f
}

func (f *tlsFixture) lastSNI() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sni) == 0 {
		return ""
	}
	return f.sni[len(f.sni)-1]
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	stamp(w, "text/plain")
	_, _ = io.WriteString(w, "trusted\n")
}

func TestCurlScopedTrustAddsATestCA(t *testing.T) {
	f := newTLSFixture(t, nil, okHandler)
	h := newHarness(t)
	h.write("ca.pem", string(f.ca.PEM))
	testutil.FailErr(t, "mkdir capath", os.MkdirAll(h.path("certs"), 0o755))
	h.write("certs/internal-ca.pem", string(f.ca.PEM))
	h.write("certs/README", "not a certificate\n")
	testutil.FailErr(t, "hashed link", os.Symlink("internal-ca.pem", h.path("certs/5a1c0f9e.0")))
	h.write("unrelated.pem", "-----BEGIN NOTHING-----\n")

	r := h.curl(t.Context(), "-sS", f.server.URL)
	if r.result.Status != 60 || f.hits.Load() != 0 || !contains(r.stderr, "curl: (60) SSL certificate problem") || r.result.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("untrusted CA reached the server: %+v %q hits=%d", r.result, r.stderr, f.hits.Load())
	}
	for _, trust := range [][]string{{"--cacert", "ca.pem"}, {"--capath", "certs"}} {
		r := h.curl(t.Context(), append(append([]string{"-s"}, trust...), f.server.URL)...)
		if r.err != nil || r.result.Status != 0 || r.stdout != "trusted\n" {
			t.Fatalf("%v did not add trust: %+v %v %q", trust, r.result, r.err, r.stderr)
		}
	}
	// The added trust is scoped to the request that named it.
	if r := h.curl(t.Context(), "-s", f.server.URL); r.result.Status != 60 {
		t.Fatalf("trust leaked into a later request: %+v", r.result)
	}
	if r := h.curl(t.Context(), "-sS", "--cacert", "unrelated.pem", f.server.URL); r.result.Status != 77 || !contains(r.stderr, "curl: (77) error setting certificate verify locations") || f.hits.Load() != 2 {
		t.Fatalf("unusable bundle: %+v %q", r.result, r.stderr)
	}
	for _, args := range [][]string{{"-k", "--cacert", "ca.pem"}, {"--cacert", "ca.pem", "--insecure"}} {
		r := h.curl(t.Context(), append(args, f.server.URL)...)
		if reject := toolrejection.AsToolReject(r.err); reject == nil || reject.Code != "COMMAND_TLS_VERIFICATION_REQUIRED" {
			t.Fatalf("%v did not refuse: %v", args, r.err)
		}
	}
	if f.hits.Load() != 2 {
		t.Fatalf("refused requests reached the server: %d", f.hits.Load())
	}
}

func TestCurlClientCertificateAndPassphrase(t *testing.T) {
	clients := tlstest.NewCA(t, "Client CA")
	f := newTLSFixture(t, clients, okHandler)
	client := clients.Client(t, "deploy-bot")
	const passphrase = "fixture-mtls-passphrase-7c41d2"
	h := newHarness(t)
	h.write("ca.pem", string(f.ca.PEM))
	h.write("client.pem", string(client.CertPEM))
	h.write("client.key", string(tlstest.EncryptPKCS8(t, client.KeyDER, passphrase)))
	h.write("plain.key", string(client.KeyPEM))
	h.write("bundle.pem", string(client.CertPEM)+string(client.KeyPEM))

	r := h.curl(t.Context(), "-sS", "--cacert", "ca.pem", f.server.URL)
	if r.result.Status != 56 || f.hits.Load() != 0 || !contains(r.stderr, "curl: (56)") {
		t.Fatalf("mTLS server accepted a request without a certificate: %+v %q", r.result, r.stderr)
	}
	accepted := [][]string{
		{"--cert", "client.pem", "--key", "client.key", "--pass", passphrase},
		{"-E", "client.pem:" + passphrase, "--key", "client.key", "--cert-type", "PEM"},
		{"--cert", "client.pem", "--key", "plain.key"},
		{"--cert", "bundle.pem"},
	}
	var runs []run
	for _, identity := range accepted {
		r := h.curl(t.Context(), append(append([]string{"-sS", "-v", "--cacert", "ca.pem"}, identity...), f.server.URL)...)
		if r.err != nil || r.result.Status != 0 || r.stdout != "trusted\n" {
			t.Fatalf("%v rejected: %+v %v %q", identity, r.result, r.err, r.stderr)
		}
		runs = append(runs, r)
	}
	f.mu.Lock()
	peers := strings.Join(f.peers, ",")
	f.mu.Unlock()
	if peers != "deploy-bot,deploy-bot,deploy-bot,deploy-bot" {
		t.Fatalf("server saw client identities %q", peers)
	}
	for _, bad := range [][]string{{"--pass", "wrong"}, {}} {
		r := h.curl(t.Context(), append(append([]string{"-sS", "--cacert", "ca.pem", "--cert", "client.pem", "--key", "client.key"}, bad...), f.server.URL)...)
		if r.result.Status != 58 || r.result.EffectCertainty != toolcontract.NoEffect || !contains(r.stderr, "curl: (58) unable to use client certificate") {
			t.Fatalf("%v opened the key: %+v %q", bad, r.result, r.stderr)
		}
		runs = append(runs, r)
	}
	if r := h.curl(t.Context(), "--cert-type", "DER", "--cert", "client.pem", f.server.URL); toolrejection.AsToolReject(r.err) == nil || toolrejection.AsToolReject(r.err).Code != "COMMAND_OPTION_UNSUPPORTED" {
		t.Fatalf("--cert-type DER accepted: %v", r.err)
	}
	if f.hits.Load() != 4 {
		t.Fatalf("server hits %d", f.hits.Load())
	}

	// The passphrase is never written by curl, and what the transcript and the
	// retained review receive passes the program's screen, which holds the
	// resolved reference value.
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Name: "MTLS_PASSPHRASE", Secret: passphrase, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}}
	})
	projector := captureprojection.New(matcher, nil)
	scope := captureprojection.Scope{ProjectID: "p1", RootSessionID: h.tc.Identity.SessionID, SessionID: h.tc.Identity.SessionID}
	for _, r := range runs {
		evidence, err := json.Marshal(r.evidence)
		testutil.FailErr(t, "encode evidence", err)
		for _, out := range []string{r.stdout, r.stderr, errorText(r.err), string(evidence), r.transfer.SentSHA256} {
			if strings.Contains(out, passphrase) {
				t.Fatalf("curl wrote the passphrase: %q", out)
			}
		}
		review, err := json.Marshal(r.review.Arguments())
		testutil.FailErr(t, "encode review", err)
		screened, err := projector.Text(t.Context(), scope, "operation.arguments", string(review))
		testutil.FailErr(t, "screen review", err)
		if strings.Contains(screened.Value, passphrase) || (strings.Contains(string(review), passphrase) && screened.Metadata.RedactedCount == 0) {
			t.Fatalf("retained review carries the passphrase: %s", screened.Value)
		}
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestCurlDialOverridesKeepTheURLHost(t *testing.T) {
	f := newTLSFixture(t, nil, func(w http.ResponseWriter, r *http.Request) {
		stamp(w, "text/plain")
		_, _ = io.WriteString(w, "host="+r.Host+"\n")
	})
	port, err := strconv.ParseUint(f.port, 10, 16)
	testutil.FailErr(t, "port", err)
	h := newHarness(t)
	h.write("ca.pem", string(f.ca.PEM))
	h.tc.Local.LoopbackConnectGranted = false
	h.tc.Local.OwnedLoopbackPorts = []uint16{uint16(port)}

	for _, c := range []struct {
		name, url, host string
		args            []string
	}{
		{"resolve", "https://app.example.test:" + f.port + "/", "app.example.test:" + f.port, []string{"--resolve", "app.example.test:" + f.port + ":127.0.0.1"}},
		{"resolve at 443 through connect-to", "https://app.example.test/", "app.example.test", []string{"--resolve", "app.example.test:" + f.port + ":127.0.0.1", "--connect-to", "app.example.test:443:app.example.test:" + f.port}},
		{"connect-to address", "https://app.example.test/", "app.example.test", []string{"--connect-to", "::127.0.0.1:" + f.port}},
		{"connect-to localhost", "https://app.example.test/", "app.example.test", []string{"--connect-to", "app.example.test:443:localhost:" + f.port}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.curl(t.Context(), append(append([]string{"-s", "--cacert", "ca.pem", "-w", "%{remote_ip}:%{remote_port}"}, c.args...), c.url)...)
			if r.err != nil || r.stdout != "host="+c.host+"\n127.0.0.1:"+f.port || f.lastSNI() != "app.example.test" {
				t.Fatalf("override %+v %v stdout=%q sni=%q", r.result, r.err, r.stdout, f.lastSNI())
			}
		})
	}
	hits := f.hits.Load()
	h.tc.Local.OwnedLoopbackPorts = nil
	r := h.curl(t.Context(), "-s", "--cacert", "ca.pem", "--resolve", "app.example.test:"+f.port+":127.0.0.1", "--connect-to", "app.example.test:443:app.example.test:"+f.port, "https://app.example.test/")
	if reject := toolrejection.AsToolReject(r.err); r.result.StopCode != "SANDBOX_TRY_LOOPBACK_CONNECT" || reject == nil || reject.Data["port"] != uint16(port) || f.hits.Load() != hits {
		t.Fatalf("override reached loopback without authority: %+v %v", r.result, r.err)
	}
	r = h.curl(t.Context(), "-s", "--resolve", "app.example.test:443:10.0.0.7", "https://app.example.test/")
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "HTTP_REQUEST_HOST_DENIED" || r.result.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("private override address passed the destination policy: %+v %v", r.result, r.err)
	}
	for _, bad := range [][]string{{"--resolve", "app.example.test:443"}, {"--resolve", "app.example.test:443:not-an-ip"}, {"--resolve", "app.example.test:443:10.0.0.1,10.0.0.2"}, {"--connect-to", "a:1:b"}, {"--connect-to", "a:x:b:2"}} {
		if reject := toolrejection.AsToolReject(h.curl(t.Context(), append(bad, "https://app.example.test/")...).err); reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" {
			t.Fatalf("%v accepted", bad)
		}
	}
}

func TestCurlVerboseScreensCredentials(t *testing.T) {
	const token = "fixture-bearer-4d9e1a7b"
	const apiKey = "fixture-api-key-0b6f33"
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		stamp(w, "text/plain")
		w.Header().Set("X-Seen", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, "ok\n")
	})
	h := newHarness(t)
	r := h.curl(t.Context(), "-s", "-v", "-H", "Authorization: Bearer "+token, "-H", "X-Api-Key: "+apiKey, server.URL+"/path?q=1")
	if r.err != nil || r.stdout != "ok\n" {
		t.Fatalf("verbose request failed: %+v %v", r.result, r.err)
	}
	host := strings.TrimPrefix(server.URL, "http://")
	for _, want := range []string{
		"*   Trying " + host + "...\n",
		"* Connected to 127.0.0.1 (127.0.0.1) port " + host[strings.LastIndexByte(host, ':')+1:] + "\n",
		"> GET /path?q=1 HTTP/1.1\r\n",
		"> Host: " + host + "\r\n",
		"> Authorization: Bearer [REDACTED]\r\n",
		"> X-Api-Key: " + apiKey + "\r\n",
		"> \r\n",
		"< HTTP/1.1 200 OK\r\n",
		"< Content-Type: text/plain\r\n",
		"< \r\n",
	} {
		if !contains(r.stderr, want) {
			t.Fatalf("verbose output lacks %q:\n%s", want, r.stderr)
		}
	}
	if strings.Count(r.stderr, token) != 1 || !contains(r.stderr, "< X-Seen: Bearer "+token) {
		t.Fatalf("the credential was not sent, or -v printed the request's credential:\n%s", r.stderr)
	}
	// What reaches the model passes the program's output screen, which masks
	// resolved secret values wherever they appear.
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{Name: "API_TOKEN", Secret: token, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true},
			{Name: "API_KEY", Secret: apiKey, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true},
		}
	})
	scope := captureprojection.Scope{ProjectID: "p1", RootSessionID: h.tc.Identity.SessionID, SessionID: h.tc.Identity.SessionID}
	screened, err := captureprojection.New(matcher, nil).Text(t.Context(), scope, "capture.native", r.stderr)
	testutil.FailErr(t, "screen stderr", err)
	if strings.Contains(screened.Value, token) || strings.Contains(screened.Value, apiKey) || !contains(screened.Value, "> Authorization: Bearer [REDACTED]") {
		t.Fatalf("screened verbose output:\n%s", screened.Value)
	}

	f := newTLSFixture(t, nil, okHandler)
	h.write("ca.pem", string(f.ca.PEM))
	r = h.curl(t.Context(), "-s", "-v", "--cacert", "ca.pem", f.server.URL)
	for _, want := range []string{"* SSL connection using TLSv1.3 / TLS_", "* ALPN: server accepted http/1.1\n", "*  subject: CN=app.example.test\n", "*  issuer: CN=Painted Wolf Test CA\n"} {
		if r.err != nil || !contains(r.stderr, want) {
			t.Fatalf("TLS summary lacks %q: %v\n%s", want, r.err, r.stderr)
		}
	}
}
