package curl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// closedURL names a loopback port nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "reserve closed port", err)
	closed := "http://" + listener.Addr().String() + "/"
	_ = listener.Close()
	return closed
}

// fixedServer answers deterministically so header bytes compare exactly.
func fixedServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func stamp(w http.ResponseWriter, contentType string) {
	w.Header().Set("Date", "Tue, 20 Oct 2026 12:00:00 GMT")
	w.Header().Set("Content-Type", contentType)
}

func TestCurlBodyHeadersAndWriteOut(t *testing.T) {
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		stamp(w, "text/plain")
		w.Header().Set("X-Order", "kept")
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "not here\n")
			return
		}
		_, _ = io.WriteString(w, "hello body\n")
	})
	h := newHarness(t)
	// Received order and spelling: the server writes Content-Length last.
	headers := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nDate: Tue, 20 Oct 2026 12:00:00 GMT\r\nX-Order: kept\r\nContent-Length: 11\r\n\r\n"
	cases := []struct {
		name   string
		args   []string
		stdout string
		status int
	}{
		{"body", []string{"-s", server.URL + "/ok"}, "hello body\n", 0},
		{"include", []string{"-s", "-i", server.URL + "/ok"}, headers + "hello body\n", 0},
		{"head", []string{"-s", "-I", server.URL + "/ok"}, headers, 0},
		{"dump headers to stdout", []string{"-s", "-D", "-", "-o", "/dev/null", server.URL + "/ok"}, headers, 0},
		{"write-out", []string{"-s", "-o", "/dev/null", "-w", `%{http_code} %{size_download} %{url_effective}\n%%`, server.URL + "/ok"}, "200 11 " + server.URL + "/ok\n%", 0},
		{"http error without --fail", []string{"-s", server.URL + "/missing"}, "not here\n", 0},
		{"--fail suppresses the body", []string{"-s", "--fail", "-w", "%{http_code}", server.URL + "/missing"}, "404", 22},
		{"scheme defaults to http", []string{"-s", strings.TrimPrefix(server.URL, "http://") + "/ok"}, "hello body\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.curl(t.Context(), c.args...)
			if r.stdout != c.stdout || r.result.Status != c.status {
				t.Fatalf("stdout %q status %d err %v", r.stdout, r.result.Status, r.err)
			}
			if c.status == 0 && (r.err != nil || r.result.EffectCertainty != toolcontract.EffectCompleted || !r.result.PublicationEligible) {
				t.Fatalf("success settled as %+v %v", r.result, r.err)
			}
		})
	}
	if r := h.curl(t.Context(), "-S", "--fail", server.URL+"/missing"); !contains(r.stderr, "curl: (22)") {
		t.Fatalf("--fail error not reported: %q", r.stderr)
	}
	if r := h.curl(t.Context(), "--not-a-version-1-option", server.URL); r.err == nil {
		t.Fatal("unsupported option bound")
	}
}

func TestCurlRequestBytes(t *testing.T) {
	type seen struct {
		method, contentType, body, auth, cookie, header string
	}
	var mu sync.Mutex
	var last seen
	var lastFiles map[string]string
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		s := seen{method: r.Method, contentType: r.Header.Get("Content-Type"), auth: r.Header.Get("Authorization"), cookie: r.Header.Get("Cookie"), header: r.Header.Get("X-Custom")}
		files := map[string]string{}
		if media, params, _ := mime.ParseMediaType(s.contentType); media == "multipart/form-data" {
			reader := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := reader.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(part)
				files[part.FormName()+":"+part.FileName()] = string(data)
			}
		} else {
			data, _ := io.ReadAll(r.Body)
			s.body = string(data)
		}
		mu.Lock()
		last, lastFiles = s, files
		mu.Unlock()
		stamp(w, "text/plain")
	})
	h := newHarness(t)
	h.write("payload.bin", "line one\nline two\n")
	h.write("upload.txt", "uploaded\n")
	cases := []struct {
		name string
		args []string
		want seen
	}{
		{"data joins with ampersand", []string{"-d", "a=1", "-d", "b=2"}, seen{method: "POST", contentType: "application/x-www-form-urlencoded", body: "a=1&b=2"}},
		{"data @file strips newlines", []string{"-d", "@payload.bin"}, seen{method: "POST", contentType: "application/x-www-form-urlencoded", body: "line oneline two"}},
		{"binary keeps bytes", []string{"--data-binary", "@payload.bin", "-H", "Content-Type: application/octet-stream"}, seen{method: "POST", contentType: "application/octet-stream", body: "line one\nline two\n"}},
		{"raw never reads a file", []string{"--data-raw", "@payload.bin"}, seen{method: "POST", contentType: "application/x-www-form-urlencoded", body: "@payload.bin"}},
		{"urlencode forms", []string{"--data-urlencode", "q=a b&c", "--data-urlencode", "=x y"}, seen{method: "POST", contentType: "application/x-www-form-urlencoded", body: "q=a+b%26c&x+y"}},
		{"explicit method and header", []string{"-X", "PUT", "-H", "X-Custom: kept value", "-d", "{}", "-H", "Content-Type: application/json"}, seen{method: "PUT", contentType: "application/json", body: "{}", header: "kept value"}},
		{"basic auth", []string{"-u", "alice:s3cret"}, seen{method: "GET", auth: "Basic YWxpY2U6czNjcmV0"}},
		{"cookie pairs", []string{"-b", "a=1", "-b", "b=2"}, seen{method: "GET", cookie: "a=1; b=2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.curl(t.Context(), append([]string{"-s"}, append(c.args, server.URL+"/echo")...)...)
			if r.err != nil {
				t.Fatalf("curl: %v", r.err)
			}
			mu.Lock()
			got := last
			mu.Unlock()
			if got != c.want {
				t.Fatalf("server saw %+v, want %+v", got, c.want)
			}
		})
	}
	r := h.curl(t.Context(), "-s", "-F", "note=hello", "-F", "doc=@upload.txt;type=text/plain", server.URL+"/upload")
	mu.Lock()
	files := lastFiles
	mu.Unlock()
	if r.err != nil || files["note:"] != "hello" || files["doc:upload.txt"] != "uploaded\n" {
		t.Fatalf("multipart parts %v %v", files, r.err)
	}
}

func TestCurlOutputFilesPublishExactBytesAndPins(t *testing.T) {
	body := []byte{0x89, 'P', 'N', 'G', 0, 0xff, '\r', '\n'}
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		stamp(w, "image/png")
		_, _ = w.Write(body)
	})
	h := newHarness(t)
	r := h.curl(t.Context(), "-s", "-o", "img.png", "-D", "headers.txt", server.URL+"/img")
	if r.err != nil || r.stdout != "" {
		t.Fatalf("curl -o: %v %q", r.err, r.stdout)
	}
	if got := h.read("img.png"); got != string(body) {
		t.Fatalf("-o published % x", got)
	}
	if !strings.HasPrefix(h.read("headers.txt"), "HTTP/1.1 200 OK\r\n") {
		t.Fatalf("-D file %q", h.read("headers.txt"))
	}
	if pins := h.pins.pins[r.execution]; len(pins) != 2 || pins[0].Path != "img.png" || pins[1].Path != "headers.txt" || pins[0].Mode != toolcontract.AbsentRevision {
		t.Fatalf("destination pins %+v", pins)
	}
	sum := sha256.Sum256(body)
	if r.transfer.ReceivedSHA256 != hex.EncodeToString(sum[:]) || r.transfer.EmittedSHA256 != r.transfer.ReceivedSHA256 {
		t.Fatalf("transfer identity %+v", r.transfer)
	}
}

func TestCurlCookieJarRoundTrip(t *testing.T) {
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		stamp(w, "text/plain")
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: "theme", Value: "dark", Path: "/prefs"})
		case "/me":
			c, err := r.Cookie("session")
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, "user for "+c.Value+"\n")
		}
	})
	h := newHarness(t)
	login := h.curl(t.Context(), "-s", "-D", "-", "-c", "jar.txt", server.URL+"/login")
	// Header dumps and cookie files preserve the server values.
	if login.err != nil || !contains(login.stdout, "Set-Cookie: session=abc123") || !strings.HasPrefix(login.stdout, "HTTP/1.1 200 OK\r\n") {
		t.Fatalf("login: %v %q", login.err, login.stdout)
	}
	jar := h.read("jar.txt")
	if !strings.HasPrefix(jar, "# Netscape HTTP Cookie File") || !contains(jar, "#HttpOnly_127.0.0.1\tFALSE\t/\tFALSE\t0\tsession\tabc123") || !contains(jar, "127.0.0.1\tFALSE\t/prefs\tFALSE\t0\ttheme\tdark") {
		t.Fatalf("jar file %q", jar)
	}
	r := h.curl(t.Context(), "-s", "-b", "jar.txt", "-c", "jar.txt", "-o", "me.txt", "-D", "me.hdr", server.URL+"/me")
	if r.err != nil || h.read("me.txt") != "user for abc123\n" {
		t.Fatalf("cookie round trip %q %v", h.read("me.txt"), r.err)
	}
	if pins := h.pins.pins[r.execution]; len(pins) != 4 || pins[0].Path != "me.txt" || pins[1].Path != "me.hdr" || pins[2].Path != "jar.txt" || pins[2].Role != toolcontract.DestinationRevision || pins[3].Path != "jar.txt" || pins[3].Role != toolcontract.SourceRevision {
		t.Fatalf("jar capture and read pins %+v", pins)
	}
}

func TestCurlExitStatusesAndUnknownOutcomes(t *testing.T) {
	var hits atomic.Int32
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/slow":
			time.Sleep(2 * time.Second)
		case "/drop":
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	})
	closed := closedURL(t)
	h := newHarness(t)
	cases := []struct {
		name      string
		args      []string
		status    int
		certainty toolcontract.EffectCertainty
		hits      int32
	}{
		{"connect refused", []string{closed}, 7, toolcontract.NoEffect, 0},
		{"dns failure", []string{"http://curl-fixture.invalid/"}, 6, toolcontract.NoEffect, 0},
		{"timeout after send is unknown", []string{"--max-time", "0.3", server.URL + "/slow"}, 28, toolcontract.EffectUnknown, 1},
		{"no reply after send is unknown", []string{server.URL + "/drop"}, 52, toolcontract.EffectUnknown, 1},
		{"redirect loop", []string{"-L", server.URL + "/loop"}, 47, toolcontract.EffectCompleted, 11},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits.Store(0)
			r := h.curl(t.Context(), append([]string{"-s"}, c.args...)...)
			if r.result.Status != c.status || r.result.EffectCertainty != c.certainty || r.result.StopCode != "" || r.err == nil {
				t.Fatalf("settled %+v %v", r.result, r.err)
			}
			if got := hits.Load(); got != c.hits {
				t.Fatalf("server saw %d requests, want %d (no retry)", got, c.hits)
			}
		})
	}
	if r := h.curl(t.Context(), "-s", server.URL+"/drop"); r.stderr != "" {
		t.Fatalf("-s printed an error: %q", r.stderr)
	}
	if r := h.curl(t.Context(), "-sS", server.URL+"/drop"); !contains(r.stderr, "curl: (52)") {
		t.Fatalf("-sS hid the error: %q", r.stderr)
	}
	code, result := TransportOutcome(&httpaction.TransportFailure{Reject: &toolrejection.ToolReject{Code: "HTTP_REQUEST_FAILED"},
		Cause: &outboundhttp.ExchangeError{Method: "POST", RequestsStarted: 1, Err: io.ErrUnexpectedEOF}})
	if code != 52 || result.EffectCertainty != toolcontract.EffectUnknown || result.Validate() != nil {
		t.Fatalf("transport outcome %d %+v", code, result)
	}
}

func serverPort(t *testing.T, rawURL string) uint16 {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	testutil.FailErr(t, "split server host:port", err)
	port, err := strconv.ParseUint(portText, 10, 16)
	testutil.FailErr(t, "parse server port", err)
	return uint16(port)
}

func TestCurlLoopbackAndEgressAuthority(t *testing.T) {
	var targetHits atomic.Int32
	target := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("credentials crossed origins: %q", r.Header.Get("Authorization"))
		}
		stamp(w, "text/plain")
		_, _ = io.WriteString(w, "landed\n")
	})
	origin := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/next", http.StatusFound)
	})
	originPort, targetPort := serverPort(t, origin.URL), serverPort(t, target.URL)
	h := newHarness(t)
	h.tc.Local.LoopbackConnectGranted = false
	r := h.curl(t.Context(), "-s", origin.URL)
	if reject := toolrejection.AsToolReject(r.err); r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "SANDBOX_TRY_LOOPBACK_CONNECT" || r.result.EffectCertainty != toolcontract.NoEffect || !contains(r.stderr, "SANDBOX_TRY_LOOPBACK_CONNECT") || reject == nil || reject.Data["port"] != originPort {
		t.Fatalf("loopback without authority %+v %v stderr=%q", r.result, r.err, r.stderr)
	}
	// OwnedLoopbackPorts admits only the owned listener port and refuses a redirect to another local port.
	h.tc.Local.OwnedLoopbackPorts = []uint16{originPort}
	if r := h.curl(t.Context(), "-s", origin.URL); r.err != nil || r.result.Status != 0 {
		t.Fatalf("owned loopback origin failed: %+v %v", r.result, r.err)
	}
	r = h.curl(t.Context(), "-s", "-L", origin.URL)
	if reject := toolrejection.AsToolReject(r.err); r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "SANDBOX_TRY_LOOPBACK_CONNECT" || r.result.EffectCertainty != toolcontract.EffectCompleted || targetHits.Load() != 0 || reject == nil || reject.Data["port"] != targetPort {
		t.Fatalf("redirect to ungranted loopback port: %+v %v hits=%d", r.result, r.err, targetHits.Load())
	}
	h.tc.Local.OwnedLoopbackPorts = nil
	h.tc.Local.LoopbackConnectGranted = true

	var mu sync.Mutex
	var asked []string
	allow := map[string]bool{}
	confine.SetEgressPosture(confine.PostureAsk)
	release := confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		mu.Lock()
		defer mu.Unlock()
		endpoint := net.JoinHostPort(ep.Host, fmt.Sprint(ep.Port))
		asked = append(asked, endpoint)
		return allow[endpoint]
	})
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		_ = release(context.Background())
	})
	originHost := strings.TrimPrefix(origin.URL, "http://")
	targetHost := strings.TrimPrefix(target.URL, "http://")
	allow[originHost] = true
	r = h.curl(t.Context(), "-s", "-L", "-H", "Authorization: Bearer origin-only", origin.URL)
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "HTTP_REQUEST_HOST_DENIED" || r.result.EffectCertainty != toolcontract.EffectCompleted || targetHits.Load() != 0 {
		t.Fatalf("redirect reached a denied host or hid completed initial hop: %+v hits=%d", r.result, targetHits.Load())
	}
	mu.Lock()
	if len(asked) != 2 || asked[0] != originHost || asked[1] != targetHost {
		t.Fatalf("hops were not authorized separately: %v", asked)
	}
	asked = nil
	allow[targetHost] = true
	mu.Unlock()
	h.tc.Identity.SessionID += "-approved"
	r = h.curl(t.Context(), "-s", "-L", "-H", "Authorization: Bearer origin-only", origin.URL)
	if r.err != nil || r.stdout != "landed\n" || targetHits.Load() != 1 {
		t.Fatalf("approved redirect %+v %v %q", r.result, r.err, r.stdout)
	}
}

// Differential against the system curl over a loopback server only: the
// status line, header block, -w output and body bytes must match exactly.
func TestCurlMatchesSystemCurlOnLoopback(t *testing.T) {
	system, err := exec.LookPath("/usr/bin/curl")
	if err != nil {
		t.Skip("no /usr/bin/curl on this host")
	}
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		if hops := strings.TrimPrefix(r.URL.Path, "/hop/"); hops != r.URL.Path && hops != "0" {
			stamp(w, "text/plain")
			next, _ := strconv.Atoi(hops)
			w.Header().Set("Location", fmt.Sprintf("/hop/%d", next-1))
			w.WriteHeader(http.StatusFound)
			return
		}
		switch r.URL.Path {
		case "/fail":
			stamp(w, "text/plain")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "failure detail\n")
			return
		case "/digits":
			http.ServeContent(w, r, "digits.txt", time.Unix(0, 0), strings.NewReader("0123456789"))
			return
		}
		if r.URL.Path == "/echo" {
			body, _ := io.ReadAll(r.Body)
			accept := r.Header.Get("Accept")
			if accept == "*/*" {
				accept = ""
			}
			stamp(w, "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "%s %s\nctype=%s\naccept=%s\nauth=%s\nua=%s\ncustom=%s\nbody=%s\n",
				r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), accept,
				r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("X-Custom"), string(body))
			return
		}
		stamp(w, "application/json")
		w.Header().Set("X-Request", r.Method+" "+r.URL.Path)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"ok":true}`+"\n")
	})
	h := newHarness(t)
	h.write("upload.txt", "uploaded bytes\n")
	facts := `%{http_code} %{http_version} %{num_redirects} %{redirect_url} %{content_type} %{remote_ip} %{remote_port} %{size_header} %{size_upload} %{exitcode}\n`
	for _, args := range [][]string{
		{"-s", "-D", "-", "-o", "/dev/null", "-w", `%{http_code} %{size_download} %{url_effective}\n`, server.URL + "/a"},
		{"-s", "-o", "/dev/null", "-w", facts, server.URL + "/hop/1"},
		{"-s", "-L", "-o", "/dev/null", "-w", facts, server.URL + "/hop/2"},
		{"-s", "-A", "probe/1", "-T", "upload.txt", "-w", facts, server.URL + "/echo"},
		{"-s", "-A", "probe/1", "-T", "upload.txt", "-X", "POST", server.URL + "/echo"},
		{"-s", "--fail-with-body", "-w", facts, server.URL + "/fail"},
		{"-s", "--fail", "-w", facts, server.URL + "/fail"},
		{"-s", "-r", "3-5", "-w", " %{http_code}", server.URL + "/digits"},
		{"-s", "-r", "40-", "-o", "/dev/null", "-w", "%{http_code}", server.URL + "/digits"},
		{"-s", "-i", server.URL + "/b"},
		{"-s", "-I", server.URL + "/c"},
		{"-s", "-w", `[%{http_code}]\t%%\n`, server.URL + "/d"},
		{"-s", "-L", "-D", "-", "-o", "/dev/null", "-w", `%{http_code} %{url_effective}\n`, server.URL + "/hop/2"},
		{"-s", "-L", "-i", server.URL + "/hop/1"},
		{"-s", "-L", "-I", server.URL + "/hop/2"},
		{"-s", "-D", "-", server.URL + "/hop/1"},
		{"-s", "-A", "probe/1", "-d", "alpha=1", "-d", "beta=2", server.URL + "/echo"},
		{"-s", "-A", "probe/1", "--data-urlencode", "msg=hello world&more", server.URL + "/echo"},
		{"-s", "-A", "probe/1", "-G", "-d", "k=v", "--data-urlencode", "q=a b", server.URL + "/echo"},
		{"-s", "-A", "probe/1", "--json", `{"x":1}`, server.URL + "/echo"},
		{"-s", "-A", "probe/1", "-u", "user:pass", "-H", "X-Custom: val", server.URL + "/echo"},
	} {
		cmd := exec.Command(system, append([]string{"--noproxy", "*"}, args...)...)
		cmd.Env = append(os.Environ(), "NO_PROXY=*", "no_proxy=*", "CURL_HOME=/nonexistent")
		cmd.Dir = h.root
		want, err := cmd.Output()
		status := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			status = exit.ExitCode()
		} else {
			testutil.FailErr(t, "run system curl", err)
		}
		r := h.curl(t.Context(), args...)
		if r.result.Status != status || r.stdout != string(want) {
			t.Fatalf("%v differs from system curl:\n got %q (%d)\nwant %q (%d) %v", args, r.stdout, r.result.Status, want, status, r.err)
		}
	}
}

func TestCurlRefusesInvalidUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{}, {"http://a.test", "http://b.test"}, {"-I", "-d", "x", "http://a.test"}, {"-F", "a=1", "-d", "b", "http://a.test"}, {"-u", "nocolon", "http://a.test"}, {"--max-time", "-1", "http://a.test"}} {
		r := h.curl(t.Context(), args...)
		reject := toolrejection.AsToolReject(r.err)
		if reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" || r.result.EffectCertainty == toolcontract.EffectCompleted {
			t.Fatalf("%v accepted: %+v %v", args, r.result, r.err)
		}
	}
}

func TestCurlDirectIPHonorsDeclaredDestinationPolicy(t *testing.T) {
	h := newHarness(t)
	denied := h.curl(t.Context(), "-s", "--connect-timeout", "0.05", "http://192.0.2.1:8080/health")
	reject := toolrejection.AsToolReject(denied.err)
	if reject == nil || reject.Code != "HTTP_REQUEST_HOST_DENIED" {
		t.Fatalf("ungranted private destination = %v (reject=%+v), want HTTP_REQUEST_HOST_DENIED", denied.err, reject)
	}

	h.tc.Direct.DirectIPAuthorized = true
	h.tc.Direct.DirectIPDeclared = []string{"192.0.2.1:9090"}
	mismatch := h.curl(t.Context(), "-s", "--connect-timeout", "0.05", "http://192.0.2.1:8080/health")
	reject = toolrejection.AsToolReject(mismatch.err)
	if reject == nil || reject.Code != "HTTP_REQUEST_HOST_DENIED" {
		t.Fatalf("mismatched declared port = %v (reject=%+v), want HTTP_REQUEST_HOST_DENIED", mismatch.err, reject)
	}

	h.tc.Direct.DirectIPDeclared = []string{"192.0.2.1:8080"}
	metadata := h.curl(t.Context(), "-s", "--connect-timeout", "0.05", "http://169.254.169.254:8080/latest/meta-data")
	reject = toolrejection.AsToolReject(metadata.err)
	if reject == nil || reject.Code != "HTTP_REQUEST_HOST_DENIED" {
		t.Fatalf("link-local metadata under direct_ip = %v (reject=%+v), want HTTP_REQUEST_HOST_DENIED", metadata.err, reject)
	}
}

func TestCurlExchangeProvenanceReporting(t *testing.T) {
	proxyServer := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		stamp(w, "text/plain")
		if strings.HasSuffix(r.URL.Path, "/fail") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "remote error body\n")
			return
		}
		_, _ = io.WriteString(w, "remote payload\n")
	})
	port := strconv.Itoa(int(serverPort(t, proxyServer.URL)))
	resolveArg := "api.external.example:" + port + ":127.0.0.1"
	extBase := "http://api.external.example:" + port
	h := newHarness(t)

	runObserved := func(args ...string) (run, []httpaction.ExchangeProvenance) {
		var observed []httpaction.ExchangeProvenance
		ctx := httpaction.WithExchangeProvenanceObserver(t.Context(), func(prov httpaction.ExchangeProvenance) {
			observed = append(observed, prov)
		})
		res := h.curl(ctx, args...)
		return res, observed
	}

	// 1. External stdout body reports External=true, WroteStdout=true.
	_, obs := runObserved("-s", "--resolve", resolveArg, extBase+"/data")
	if len(obs) != 1 || !obs[0].External || !obs[0].WroteStdout || obs[0].WroteStderr || len(obs[0].WrittenFiles) != 0 {
		t.Fatalf("external stdout provenance = %+v", obs)
	}

	// 2. External -o file without stdout headers reports WrittenFiles=["saved.txt"] and WroteStdout=false.
	_, obs = runObserved("-s", "--resolve", resolveArg, "-o", "saved.txt", extBase+"/data")
	if len(obs) != 1 || !obs[0].External || obs[0].WroteStdout || obs[0].WroteStderr || len(obs[0].WrittenFiles) != 1 || obs[0].WrittenFiles[0] != "saved.txt" {
		t.Fatalf("external -o provenance = %+v", obs)
	}

	// 3. External --fail -D - on 500 still reports WroteStdout=true even though exit status is 22.
	res, obs := runObserved("-s", "--resolve", resolveArg, "--fail", "-D", "-", extBase+"/fail")
	if res.result.Status != 22 || len(obs) != 1 || !obs[0].External || !obs[0].WroteStdout {
		t.Fatalf("external --fail -D - status=%d provenance=%+v", res.result.Status, obs)
	}

	// 4. External --fail -v on 500 still reports WroteStderr=true even though exit status is 22.
	res, obs = runObserved("-s", "--resolve", resolveArg, "--fail", "-v", extBase+"/fail")
	if res.result.Status != 22 || len(obs) != 1 || !obs[0].External || !obs[0].WroteStderr {
		t.Fatalf("external --fail -v status=%d provenance=%+v", res.result.Status, obs)
	}

	// 5. External --fail -w '%{http_code}' on 500 reports WroteStdout=true.
	res, obs = runObserved("-s", "--resolve", resolveArg, "--fail", "-w", "%{http_code}", extBase+"/fail")
	if res.result.Status != 22 || len(obs) != 1 || !obs[0].External || !obs[0].WroteStdout {
		t.Fatalf("external --fail -w status=%d provenance=%+v", res.result.Status, obs)
	}

	// 6. Pure loopback request reports External=false.
	_, obs = runObserved("-s", proxyServer.URL+"/local")
	if len(obs) != 1 || obs[0].External || len(obs[0].ObservedURLs) != 0 {
		t.Fatalf("loopback provenance = %+v, want External=false", obs)
	}
}

func TestCurlRedirectAdmissionRouteChangeCancellationAndUnknownHop(t *testing.T) {
	var originHits, originBytes atomic.Int32
	var targetHits, targetBytes atomic.Int32
	dropHop1AfterRequest := atomic.Bool{}
	var hostSeenMu sync.Mutex
	var originHostsSeen []string
	var targetHostsSeen []string

	target := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		targetBytes.Add(int32(len(body)))
		hostSeenMu.Lock()
		targetHostsSeen = append(targetHostsSeen, r.Host)
		hostSeenMu.Unlock()
		if dropHop1AfterRequest.Load() {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("target response writer does not support hijacking")
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		stamp(w, "text/plain")
		_, _ = io.WriteString(w, "landed\n")
	})
	targetPort := serverPort(t, target.URL)

	origin := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		originBytes.Add(int32(len(body)))
		hostSeenMu.Lock()
		originHostsSeen = append(originHostsSeen, r.Host)
		hostSeenMu.Unlock()
		switch r.URL.Path {
		case "/to-mapped":
			http.Redirect(w, r, "http://redirected.example.com/landed", http.StatusFound)
		case "/same-then-cross":
			http.Redirect(w, r, "/same-origin", http.StatusFound)
		case "/same-origin":
			http.Redirect(w, r, target.URL+"/landed", http.StatusFound)
		default:
			http.Redirect(w, r, target.URL+"/landed", http.StatusFound)
		}
	})
	originPort := serverPort(t, origin.URL)

	// 1. Explicit --connect-to on Hop 1 to an ungranted loopback port is NOT pre-granted by Hop 0's
	// loopbackPorts extraction, receives zero connections on Hop 1, preserves Hop 0's completed effect
	// without fabricating sent-body evidence from attempt counters, and succeeds once targetPort is covered.
	h := newHarness(t)
	h.tc.Local.LoopbackConnectGranted = false
	h.tc.Local.OwnedLoopbackPorts = []uint16{originPort}
	connectToArg := fmt.Sprintf("redirected.example.com:80:127.0.0.1:%d", targetPort)
	r := h.curl(t.Context(), "-s", "-L", "--connect-to", connectToArg, "-d", "payload", origin.URL+"/to-mapped")
	reject := toolrejection.AsToolReject(r.err)
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "SANDBOX_TRY_LOOPBACK_CONNECT" ||
		r.result.EffectCertainty != toolcontract.EffectCompleted || reject == nil || reject.Data["port"] != targetPort ||
		originHits.Load() != 1 || originBytes.Load() != int32(len("payload")) || targetHits.Load() != 0 {
		t.Fatalf("redirect via --connect-to to ungranted loopback port: result=%+v reject=%+v originHits=%d targetHits=%d",
			r.result, reject, originHits.Load(), targetHits.Load())
	}
	if r.evidence.EvidenceRef != "" {
		t.Fatalf("failed exchange fabricated sent-body evidence from attempt counters: %q", r.evidence.EvidenceRef)
	}
	// Covered follow-up with targetPort granted succeeds and records completed sent-body evidence.
	h.tc.Local.OwnedLoopbackPorts = []uint16{originPort, targetPort}
	r = h.curl(t.Context(), "-s", "-L", "--connect-to", connectToArg, "-d", "payload", origin.URL+"/to-mapped")
	wantSentRef := "sent:sha256:" + sha256Hex("payload")
	if r.err != nil || r.stdout != "landed\n" || originHits.Load() != 2 || targetHits.Load() != 1 || !strings.Contains(r.evidence.EvidenceRef, wantSentRef) {
		t.Fatalf("covered --connect-to redirect follow-up: err=%v stdout=%q originHits=%d targetHits=%d evidence=%q",
			r.err, r.stdout, originHits.Load(), targetHits.Load(), r.evidence.EvidenceRef)
	}

	// 2. Cancellation, real denial, and authorized recovery in the SAME session under Ask posture.
	h.tc.Local.OwnedLoopbackPorts = nil
	h.tc.Local.LoopbackConnectGranted = true
	originHits.Store(0)
	originBytes.Store(0)
	targetHits.Store(0)

	var mu sync.Mutex
	allow := map[string]bool{}
	var cancelTarget string
	var cancelFn context.CancelFunc
	confine.SetEgressPosture(confine.PostureAsk)
	release := confine.SetEgressResolver(func(ctx context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		endpoint := net.JoinHostPort(ep.Host, fmt.Sprint(ep.Port))
		mu.Lock()
		shouldCancel := cancelTarget == endpoint
		ok := allow[endpoint]
		fn := cancelFn
		mu.Unlock()
		if shouldCancel && fn != nil {
			fn()
			<-ctx.Done()
			return false
		}
		return ok
	})
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		_ = release(context.Background())
	})

	originHost := strings.TrimPrefix(origin.URL, "http://")
	targetHost := strings.TrimPrefix(target.URL, "http://")
	h.tc.Identity.SessionID = "curl-cancel-and-recovery-session"

	// 2a. Canceled while awaiting Hop 0 approval: NoEffect, Status 130, zero requests sent.
	ctx0, cancel0 := context.WithCancel(t.Context())
	mu.Lock()
	cancelTarget = originHost
	cancelFn = cancel0
	mu.Unlock()
	r = h.curl(ctx0, "-s", "-L", origin.URL)
	if r.result.Status != 130 || r.result.Terminal != toolcontract.ProducerInterrupted || r.result.EffectCertainty != toolcontract.NoEffect || originHits.Load() != 0 || targetHits.Load() != 0 {
		t.Fatalf("canceled awaiting Hop 0: result=%+v err=%v originHits=%d targetHits=%d", r.result, r.err, originHits.Load(), targetHits.Load())
	}

	// 2b. Un-canceled retry in the SAME session while Hop 0 is still denied: real denial (no false quiet grant).
	mu.Lock()
	cancelTarget = ""
	cancelFn = nil
	mu.Unlock()
	r = h.curl(t.Context(), "-s", "-L", origin.URL)
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "HTTP_REQUEST_HOST_DENIED" || r.result.EffectCertainty != toolcontract.NoEffect || originHits.Load() != 0 {
		t.Fatalf("post-cancel Hop 0 denial: result=%+v err=%v originHits=%d", r.result, r.err, originHits.Load())
	}

	// 2c. Canceled while awaiting Hop 1 approval (after Hop 0 302 completed): EffectCompleted, Status 130, Hop 1 unsent.
	ctx1, cancel1 := context.WithCancel(t.Context())
	mu.Lock()
	allow[originHost] = true
	cancelTarget = targetHost
	cancelFn = cancel1
	mu.Unlock()
	r = h.curl(ctx1, "-s", "-L", origin.URL)
	if r.result.Status != 130 || r.result.Terminal != toolcontract.ProducerInterrupted || r.result.EffectCertainty != toolcontract.EffectCompleted || originHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("canceled awaiting Hop 1: result=%+v err=%v originHits=%d targetHits=%d", r.result, r.err, originHits.Load(), targetHits.Load())
	}

	// 2d. Un-canceled retry in the SAME session while Hop 1 is still denied: real denial with EffectCompleted.
	mu.Lock()
	cancelTarget = ""
	cancelFn = nil
	mu.Unlock()
	r = h.curl(t.Context(), "-s", "-L", origin.URL)
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "HTTP_REQUEST_HOST_DENIED" || r.result.EffectCertainty != toolcontract.EffectCompleted || originHits.Load() != 2 || targetHits.Load() != 0 {
		t.Fatalf("post-cancel Hop 1 denial: result=%+v err=%v originHits=%d targetHits=%d", r.result, r.err, originHits.Load(), targetHits.Load())
	}

	// 2e. Authorizing Hop 1 in the SAME session succeeds without cached cancellation or denial poisoning.
	mu.Lock()
	allow[targetHost] = true
	mu.Unlock()
	r = h.curl(t.Context(), "-s", "-L", origin.URL)
	if r.err != nil || r.stdout != "landed\n" || originHits.Load() != 3 || targetHits.Load() != 1 {
		t.Fatalf("authorized Hop 1 recovery in same session: err=%v stdout=%q originHits=%d targetHits=%d", r.err, r.stdout, originHits.Load(), targetHits.Load())
	}

	// 3. Explicit -H "Host: vhost.example.com" across same-origin and cross-origin redirects:
	// same-origin hops preserve the explicit Host header, cross-origin hop uses the target authority
	// and still requires target host egress admission.
	originHits.Store(0)
	targetHits.Store(0)
	hostSeenMu.Lock()
	originHostsSeen = nil
	targetHostsSeen = nil
	hostSeenMu.Unlock()
	mu.Lock()
	allow[targetHost] = false
	mu.Unlock()
	r = h.curl(t.Context(), "-s", "-L", "-H", "Host: vhost.example.com", origin.URL+"/same-then-cross")
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "HTTP_REQUEST_HOST_DENIED" || r.result.EffectCertainty != toolcontract.EffectCompleted || originHits.Load() != 2 || targetHits.Load() != 0 {
		t.Fatalf("unapproved cross-origin target with Host override: result=%+v err=%v originHits=%d targetHits=%d", r.result, r.err, originHits.Load(), targetHits.Load())
	}
	originHits.Store(0)
	hostSeenMu.Lock()
	originHostsSeen = nil
	targetHostsSeen = nil
	hostSeenMu.Unlock()
	mu.Lock()
	allow[targetHost] = true
	mu.Unlock()
	r = h.curl(t.Context(), "-s", "-L", "-H", "Host: vhost.example.com", origin.URL+"/same-then-cross")
	if r.err != nil || r.stdout != "landed\n" || originHits.Load() != 2 || targetHits.Load() != 1 {
		t.Fatalf("approved Host override across same-origin then cross-origin redirect: err=%v stdout=%q originHits=%d targetHits=%d",
			r.err, r.stdout, originHits.Load(), targetHits.Load())
	}
	hostSeenMu.Lock()
	gotOriginHosts := append([]string(nil), originHostsSeen...)
	gotTargetHosts := append([]string(nil), targetHostsSeen...)
	hostSeenMu.Unlock()
	if len(gotOriginHosts) != 2 || gotOriginHosts[0] != "vhost.example.com" || gotOriginHosts[1] != "vhost.example.com" {
		t.Fatalf("same-origin hops Host headers = %v, want [vhost.example.com vhost.example.com]", gotOriginHosts)
	}
	if len(gotTargetHosts) != 1 || gotTargetHosts[0] != targetHost {
		t.Fatalf("cross-origin hop Host header = %v, want [%s]", gotTargetHosts, targetHost)
	}

	// 4. Unknown post-send outcome on Hop 1 after Hop 0 completed settles as EffectUnknown without replaying Hop 0 or Hop 1.
	originHits.Store(0)
	targetHits.Store(0)
	dropHop1AfterRequest.Store(true)
	r = h.curl(t.Context(), "-s", "-L", origin.URL)
	if r.result.Terminal != toolcontract.ProducerFailed || r.result.EffectCertainty != toolcontract.EffectUnknown || originHits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("unknown post-send outcome on Hop 1: result=%+v err=%v originHits=%d targetHits=%d", r.result, r.err, originHits.Load(), targetHits.Load())
	}
	reject = toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Data["http_requests_started"] != 2 || reject.Data["http_responses_received"] != 1 {
		t.Fatalf("unknown Hop 1 exchange progress = %+v", reject)
	}
	redirects, ok := reject.Data["redirects"].([]any)
	if !ok || len(redirects) != 1 {
		t.Fatalf("unknown Hop 1 lost Hop 0 redirect record: %#v", reject.Data["redirects"])
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestCurlCookieJarNetworkValidationAndImportSemantics(t *testing.T) {
	var seenCookies []string
	var mu sync.Mutex
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenCookies = append(seenCookies, r.Header.Get("Cookie"))
		mu.Unlock()
		stamp(w, "text/plain")
		switch r.URL.Path {
		case "/set-mixed":
			// Valid host-only IP cookies.
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "valid-ip-host", Path: "/", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: "temp", Value: "to-be-deleted", Path: "/"})
			// Invalid network Set-Cookie headers that RFC 6265 must ignore.
			w.Header().Add("Set-Cookie", "psl=bad; Domain=com; Path=/")
			w.Header().Add("Set-Cookie", "foreign=bad; Domain=other.example; Path=/")
			w.Header().Add("Set-Cookie", "ip_domain=bad; Domain=127.0.0.1; Path=/")
			w.Header().Add("Set-Cookie", "__Host-insecure=bad; Path=/")
			http.Redirect(w, r, "/delete-temp", http.StatusFound)
		case "/delete-temp":
			http.SetCookie(w, &http.Cookie{Name: "temp", Value: "", Path: "/", MaxAge: -1})
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			_, _ = io.WriteString(w, "done\n")
		case "/echo-cookie":
			_, _ = io.WriteString(w, r.Header.Get("Cookie")+"\n")
		}
	})

	h := newHarness(t)
	r := h.curl(t.Context(), "-s", "-L", "-c", "jar.txt", server.URL+"/set-mixed")
	if r.err != nil || r.stdout != "done\n" {
		t.Fatalf("set-mixed failed: %v %q", r.err, r.stdout)
	}
	jar := h.read("jar.txt")
	if !contains(jar, "#HttpOnly_127.0.0.1\tFALSE\t/\tFALSE\t0\tsession\tvalid-ip-host") {
		t.Fatalf("jar missing valid host-only IP cookie: %q", jar)
	}
	for _, forbidden := range []string{"psl", "foreign", "ip_domain", "__Host-insecure", "temp"} {
		if contains(jar, forbidden) {
			t.Fatalf("jar persisted rejected or deleted cookie %q: %q", forbidden, jar)
		}
	}
	mu.Lock()
	gotCookies := append([]string(nil), seenCookies...)
	mu.Unlock()
	if len(gotCookies) != 3 || gotCookies[1] != "session=valid-ip-host; temp=to-be-deleted" || gotCookies[2] != "session=valid-ip-host" {
		t.Fatalf("per-hop cookies across Set-Cookie and Max-Age=-1 deletion = %v", gotCookies)
	}

	// User-supplied Netscape jar import preserves host-only IP cookies, path/Secure scoping,
	// and expired-line deletion, while ignoring public-suffix wildcard domain lines.
	importedJar := strings.Join([]string{
		"# Netscape HTTP Cookie File",
		"#HttpOnly_127.0.0.1\tFALSE\t/\tFALSE\t0\tsession\timported-ip",
		"127.0.0.1\tFALSE\t/\tFALSE\t2000000000\tstale\told-val",
		"127.0.0.1\tFALSE\t/\tFALSE\t1\tstale\texpired-val",
		"127.0.0.1\tFALSE\t/admin\tFALSE\t0\tadmin_only\tsecret-admin",
		"127.0.0.1\tFALSE\t/\tTRUE\t0\tsecure_only\thttps-only",
		".com\tTRUE\t/\tFALSE\t0\tsupercookie\tpublic-suffix",
		"",
	}, "\n")
	h.write("imported.txt", importedJar)
	r = h.curl(t.Context(), "-s", "-b", "imported.txt", server.URL+"/echo-cookie")
	if r.err != nil || r.stdout != "session=imported-ip\n" {
		t.Fatalf("imported jar sent %q (err=%v), want %q", r.stdout, r.err, "session=imported-ip\n")
	}
}

func TestCurlCookieJarSelectedSecretAuthorityAndRedirectDenial(t *testing.T) {
	const (
		hop0Secret       = "protected-cookie-hop0-secret-7711"
		unselectedSecret = "protected-cookie-unselected-8822"
		targetSecret     = "protected-cookie-target-9933"
	)
	var originCookies, targetCookies []string
	var originHits, targetHits atomic.Int32
	var mu sync.Mutex

	target := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		mu.Lock()
		targetCookies = append(targetCookies, r.Header.Get("Cookie"))
		mu.Unlock()
		stamp(w, "text/plain")
		_, _ = io.WriteString(w, "target-ok\n")
	})
	origin := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		mu.Lock()
		originCookies = append(originCookies, r.Header.Get("Cookie"))
		mu.Unlock()
		stamp(w, "text/plain")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL+"/land", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "origin-ok\n")
	})

	h := newHarness(t)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	fp, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("c", 32)))
	testutil.FailErr(t, "build fingerprinter", err)
	matcher.SetFingerprinter(fp)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{Name: "HOP0_COOKIE", Secret: hop0Secret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true},
			{Name: "UNSELECTED_COOKIE", Secret: unselectedSecret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true},
			{Name: "TARGET_COOKIE", Secret: targetSecret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true},
		}
	})
	h.owners.HTTP.SecretMatcher = matcher

	var askedAlerts []secretmatch.Alert
	allowDest := map[string]bool{}
	faultDest := map[string]bool{}
	askFn := func(_ context.Context, a secretmatch.Alert) (secretmatch.Resolution, error) {
		mu.Lock()
		defer mu.Unlock()
		askedAlerts = append(askedAlerts, a)
		if faultDest[a.DestinationLabel] {
			return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageRaise, nil)
		}
		if allowDest[a.DestinationLabel] {
			return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
		}
		return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
	}

	originLabel := "http://" + strings.TrimPrefix(origin.URL, "http://")
	targetLabel := "http://" + strings.TrimPrefix(target.URL, "http://")

	// Jar contains a benign cookie (theme=light), a selected secret cookie for /public (hop0Secret),
	// an unselected path cookie (/admin -> unselectedSecret), and a path cookie for /land on target (targetSecret).
	h.write("cookies.txt", strings.Join([]string{
		"# Netscape HTTP Cookie File",
		"127.0.0.1\tFALSE\t/\tFALSE\t0\ttheme\tlight",
		"127.0.0.1\tFALSE\t/public\tFALSE\t0\tsid\t" + hop0Secret,
		"127.0.0.1\tFALSE\t/admin\tFALSE\t0\tadmin_sid\t" + unselectedSecret,
		"127.0.0.1\tFALSE\t/land\tFALSE\t0\ttarget_sid\t" + targetSecret,
		"",
	}, "\n"))

	// 1. Unwired SecretAsk on Hop 0 stops before dialing origin.
	h.owners.HTTP.SecretAsk = nil
	r := h.curl(t.Context(), "-s", "-b", "cookies.txt", origin.URL+"/public")
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != toolrejection.OutboundSecretScreenFailedCode || r.result.EffectCertainty != toolcontract.NoEffect || originHits.Load() != 0 {
		t.Fatalf("unwired Hop 0 cookie reached server: result=%+v err=%v hits=%d", r.result, r.err, originHits.Load())
	}
	h.owners.HTTP.SecretAsk = askFn

	// 2. Withhold on Hop 0 withholds only the secret cookie (sid) while preserving the benign cookie (theme=light) and continuing HTTP.
	r = h.curl(t.Context(), "-s", "-b", "cookies.txt", origin.URL+"/public")
	if r.err != nil || r.stdout != "origin-ok\n" || originHits.Load() != 1 {
		t.Fatalf("withheld Hop 0 should continue without secret cookie: result=%+v err=%v stdout=%q", r.result, r.err, r.stdout)
	}
	mu.Lock()
	if len(askedAlerts) != 1 || askedAlerts[0].DestinationLabel != originLabel || strings.Join(askedAlerts[0].SecretNames, ",") != "HOP0_COOKIE" {
		t.Fatalf("Hop 0 alerts = %+v, want only selected HOP0_COOKIE for %s", askedAlerts, originLabel)
	}
	if len(originCookies) != 1 || originCookies[0] != "theme=light" {
		t.Fatalf("withheld Hop 0 cookies = %v, want only benign [theme=light]", originCookies)
	}
	askedAlerts = nil
	originCookies = nil
	allowDest[originLabel] = true
	mu.Unlock()

	// 3. Approved on Hop 0 delivers the selected secret cookie alongside the benign cookie.
	originHits.Store(0)
	r = h.curl(t.Context(), "-s", "-b", "cookies.txt", origin.URL+"/public")
	if r.err != nil || r.stdout != "origin-ok\n" || originHits.Load() != 1 {
		t.Fatalf("approved Hop 0 failed: result=%+v err=%v stdout=%q", r.result, r.err, r.stdout)
	}
	mu.Lock()
	if len(originCookies) != 1 || originCookies[0] != "sid="+hop0Secret+"; theme=light" {
		t.Fatalf("origin received cookies %v, want [sid=%s; theme=light]", originCookies, hop0Secret)
	}
	askedAlerts = nil
	originCookies = nil
	mu.Unlock()

	// 4. Redirect from origin (/redirect) to target (/land) when target is Withhold:
	// Hop 0 completes and Hop 1 proceeds with target_sid withheld (sending only theme=light).
	originHits.Store(0)
	targetHits.Store(0)
	r = h.curl(t.Context(), "-s", "-L", "-b", "cookies.txt", origin.URL+"/redirect")
	if r.err != nil || r.stdout != "target-ok\n" || originHits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("redirect with withheld cookie failed: result=%+v err=%v originHits=%d targetHits=%d", r.result, r.err, originHits.Load(), targetHits.Load())
	}
	mu.Lock()
	if len(askedAlerts) != 1 || askedAlerts[0].DestinationLabel != targetLabel || strings.Join(askedAlerts[0].SecretNames, ",") != "TARGET_COOKIE" {
		t.Fatalf("redirect alerts = %+v, want TARGET_COOKIE for %s", askedAlerts, targetLabel)
	}
	if len(targetCookies) != 1 || targetCookies[0] != "theme=light" {
		t.Fatalf("target received cookies %v on Withhold, want only [theme=light]", targetCookies)
	}
	askedAlerts = nil
	targetCookies = nil
	faultDest[targetLabel] = true
	mu.Unlock()

	// 5. Screen fault on Hop 1 after Hop 0 completed stops before dialing target and settles with EffectCompleted.
	originHits.Store(0)
	targetHits.Store(0)
	r = h.curl(t.Context(), "-s", "-L", "-b", "cookies.txt", origin.URL+"/redirect")
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != toolrejection.OutboundSecretScreenFailedCode || r.result.EffectCertainty != toolcontract.EffectCompleted || originHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("redirect screen fault on Hop 1: result=%+v err=%v originHits=%d targetHits=%d", r.result, r.err, originHits.Load(), targetHits.Load())
	}
	mu.Lock()
	delete(faultDest, targetLabel)
	allowDest[targetLabel] = true
	askedAlerts = nil
	targetCookies = nil
	mu.Unlock()

	// 6. Approving target allows the redirect hop to deliver target_sid.
	originHits.Store(0)
	targetHits.Store(0)
	r = h.curl(t.Context(), "-s", "-L", "-b", "cookies.txt", origin.URL+"/redirect")
	if r.err != nil || r.stdout != "target-ok\n" || targetHits.Load() != 1 {
		t.Fatalf("approved redirect failed: result=%+v err=%v stdout=%q", r.result, r.err, r.stdout)
	}
	mu.Lock()
	if len(targetCookies) != 1 || targetCookies[0] != "target_sid="+targetSecret+"; theme=light" {
		t.Fatalf("target received cookies %v, want [target_sid=%s; theme=light]", targetCookies, targetSecret)
	}
	mu.Unlock()
}



