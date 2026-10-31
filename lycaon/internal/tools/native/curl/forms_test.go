package curl

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCurlBashForms(t *testing.T) {
	type seen struct {
		method, query, contentType, accept, agent, encoding, body string
	}
	var mu sync.Mutex
	var last seen
	gzipped := func(text string) []byte {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		_, _ = io.WriteString(w, text)
		_ = w.Close()
		return b.Bytes()
	}
	deflated := func(text string) []byte {
		var b bytes.Buffer
		w := zlib.NewWriter(&b)
		_, _ = io.WriteString(w, text)
		_ = w.Close()
		return b.Bytes()
	}
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = seen{method: r.Method, query: r.URL.RawQuery, contentType: r.Header.Get("Content-Type"), accept: r.Header.Get("Accept"), agent: r.Header.Get("User-Agent"), encoding: r.Header.Get("Accept-Encoding"), body: string(data)}
		mu.Unlock()
		stamp(w, "text/plain")
		switch r.URL.Path {
		case "/gzip":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(gzipped("unpacked gzip\n"))
		case "/deflate":
			w.Header().Set("Content-Encoding", "deflate")
			_, _ = w.Write(deflated("unpacked deflate\n"))
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = io.WriteString(w, "plain\n")
		}
	})
	h := newHarness(t)
	h.write("doc.json", `{"from":"file"}`)
	cases := []struct {
		name   string
		args   []string
		want   seen
		stdout string
		status int
	}{
		{name: "-f is --fail", args: []string{"-sf", server.URL + "/missing"}, want: seen{method: "GET"}, status: 22},
		{name: "-A sets the user agent", args: []string{"-s", "-A", "probe/2", server.URL}, want: seen{method: "GET", agent: "probe/2"}, stdout: "plain\n"},
		{name: "-H wins over -A", args: []string{"-s", "-A", "probe/2", "-H", "User-Agent: header/1", server.URL}, want: seen{method: "GET", agent: "header/1"}, stdout: "plain\n"},
		{name: "-G moves data into the query", args: []string{"-s", "-G", "-d", "q=a", "--data-urlencode", "r=b c", server.URL + "/find"}, want: seen{method: "GET", query: "q=a&r=b+c"}, stdout: "plain\n"},
		{name: "-G extends an existing query", args: []string{"-s", "--get", "-d", "page=2", server.URL + "/find?sort=asc"}, want: seen{method: "GET", query: "sort=asc&page=2"}, stdout: "plain\n"},
		{name: "--json sets type and accept", args: []string{"-s", "--json", `{"a":1}`, server.URL}, want: seen{method: "POST", contentType: "application/json", accept: "application/json", body: `{"a":1}`}, stdout: "plain\n"},
		{name: "--json concatenates and reads files", args: []string{"-s", "--json", "[", "--json", "@doc.json", "--json", "]", server.URL}, want: seen{method: "POST", contentType: "application/json", accept: "application/json", body: `[{"from":"file"}]`}, stdout: "plain\n"},
		{name: "--json keeps explicit headers", args: []string{"-s", "--json", "{}", "-H", "Accept: text/plain", "-X", "PUT", server.URL}, want: seen{method: "PUT", contentType: "application/json", accept: "text/plain", body: "{}"}, stdout: "plain\n"},
		{name: "--compressed decodes gzip", args: []string{"-s", "--compressed", server.URL + "/gzip"}, want: seen{method: "GET", encoding: "deflate, gzip"}, stdout: "unpacked gzip\n"},
		{name: "--compressed decodes deflate", args: []string{"-s", "--compressed", server.URL + "/deflate"}, want: seen{method: "GET", encoding: "deflate, gzip"}, stdout: "unpacked deflate\n"},
		{name: "--compressed passes identity", args: []string{"-s", "--compressed", server.URL}, want: seen{method: "GET", encoding: "deflate, gzip"}, stdout: "plain\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.curl(t.Context(), c.args...)
			if r.result.Status != c.status || r.stdout != c.stdout {
				t.Fatalf("stdout %q status %d err %v", r.stdout, r.result.Status, r.err)
			}
			mu.Lock()
			got := last
			mu.Unlock()
			if c.want.agent == "" {
				got.agent = ""
			}
			if c.want.encoding == "" {
				got.encoding = ""
			}
			if got != c.want {
				t.Fatalf("server saw %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestCurlInsecureRefusesBeforeSending(t *testing.T) {
	var requests int
	var mu sync.Mutex
	server := fixedServer(t, func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
	})
	h := newHarness(t)
	for _, spelling := range []string{"-k", "--insecure", "-sk"} {
		r := h.curl(t.Context(), spelling, server.URL)
		if rejected := toolrejection.AsToolReject(r.err); rejected == nil || rejected.Code != "COMMAND_TLS_VERIFICATION_REQUIRED" {
			t.Fatalf("%s returned %v", spelling, r.err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("-k sent %d requests", requests)
	}
}

func TestCurlCompressedRefusesUnknownCoding(t *testing.T) {
	server := fixedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		stamp(w, "text/plain")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, "opaque")
	})
	h := newHarness(t)
	if r := h.curl(t.Context(), "-sS", "--compressed", server.URL); r.result.Status != 61 || !contains(r.stderr, "curl: (61)") {
		t.Fatalf("unknown coding status %d stderr %q err %v", r.result.Status, r.stderr, r.err)
	}
}
