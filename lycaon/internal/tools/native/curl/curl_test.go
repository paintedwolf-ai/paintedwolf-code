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
	if reject := toolrejection.AsToolReject(r.err); r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "SANDBOX_TRY_LOOPBACK_CONNECT" || targetHits.Load() != 0 || reject == nil || reject.Data["port"] != targetPort {
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
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "HTTP_REQUEST_HOST_DENIED" || targetHits.Load() != 0 {
		t.Fatalf("redirect reached a denied host: %+v hits=%d", r.result, targetHits.Load())
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
