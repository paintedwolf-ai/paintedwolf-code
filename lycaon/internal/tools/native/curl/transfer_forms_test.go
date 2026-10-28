package curl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCurlUploadSendsExactBytes(t *testing.T) {
	type upload struct {
		method, path, contentType string
		body                      []byte
	}
	var mu sync.Mutex
	var got []upload
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, upload{r.Method, r.URL.Path, r.Header.Get("Content-Type"), body})
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	artifact := []byte("\x00\xffbinary\r\nartifact\x00" + strings.Repeat("z", 4096))
	sum := sha256.Sum256(artifact)
	digest := hex.EncodeToString(sum[:])
	h := newHarness(t)
	h.write("artifact.bin", string(artifact))
	cases := []struct {
		name, stdin, path, method string
		args                      []string
	}{
		{"explicit PUT", "", "/store/artifact.bin", "PUT", []string{"-T", "artifact.bin", "-X", "PUT", server.URL + "/store/artifact.bin"}},
		{"PUT by default", "", "/store/x", "PUT", []string{"--upload-file", "artifact.bin", server.URL + "/store/x"}},
		{"file name appended", "", "/store/artifact.bin", "PUT", []string{"-T", "artifact.bin", server.URL + "/store/"}},
		{"another method", "", "/dav/a", "POST", []string{"-T", "artifact.bin", "-X", "POST", server.URL + "/dav/a"}},
		{"stdin", string(artifact), "/store/", "PUT", []string{"-T", "-", server.URL + "/store/"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.curlStdin(t.Context(), c.stdin, append([]string{"-s", "-w", "%{http_code} %{size_upload}"}, c.args...)...)
			mu.Lock()
			last := got[len(got)-1]
			mu.Unlock()
			if r.err != nil || r.stdout != "201 "+strconv.Itoa(len(artifact)) || last.method != c.method || last.path != c.path || last.contentType != "" || !bytes.Equal(last.body, artifact) {
				t.Fatalf("upload %+v %v stdout=%q got %s %s %q", r.result, r.err, r.stdout, last.method, last.path, last.contentType)
			}
			if r.transfer.SentBytes != int64(len(artifact)) || r.transfer.SentSHA256 != digest || !contains(r.evidence.EvidenceRef, "sent:sha256:"+digest) {
				t.Fatalf("sent body identity %+v evidence %q", r.transfer, r.evidence.EvidenceRef)
			}
		})
	}
	for _, bad := range [][]string{{"-T", "artifact.bin", "-d", "x"}, {"-T", "artifact.bin", "-I"}, {"-T", "artifact.bin", "-G"}, {"-T", "artifact.bin", "--json", "{}"}} {
		if reject := toolrejection.AsToolReject(h.curl(t.Context(), append(bad, server.URL)...).err); reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" {
			t.Fatalf("%v accepted", bad)
		}
	}
}

func TestCurlFailWithBodyAndRange(t *testing.T) {
	content := []byte("0123456789")
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			stamp(w, "text/plain")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "stack trace here\n")
			return
		}
		http.ServeContent(w, r, "digits.txt", time.Unix(0, 0), bytes.NewReader(content))
	})
	h := newHarness(t)
	cases := []struct {
		name, stdout, stderr string
		status               int
		args                 []string
	}{
		{"fail with body", "stack trace here\n500 22", "curl: (22) The requested URL returned error: 500\n", 22, []string{"-sS", "--fail-with-body", "-w", "%{http_code} %{exitcode}", server.URL + "/broken"}},
		{"fail hides the body", "500 22", "", 22, []string{"-s", "--fail", "-w", "%{http_code} %{exitcode}", server.URL + "/broken"}},
		{"range", "234206", "", 0, []string{"-s", "-r", "2-4", "-w", "%{http_code}", server.URL + "/digits"}},
		{"suffix range", "789206", "", 0, []string{"-s", "--range", "-3", "-w", "%{http_code}", server.URL + "/digits"}},
		{"unsatisfiable range", "416", "", 0, []string{"-s", "-o", "/dev/null", "-r", "50-60", "-w", "%{http_code}", server.URL + "/digits"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.curl(t.Context(), c.args...)
			if r.stdout != c.stdout || r.stderr != c.stderr || r.result.Status != c.status {
				t.Fatalf("stdout %q stderr %q status %d err %v", r.stdout, r.stderr, r.result.Status, r.err)
			}
			if c.status == 22 && r.result.EffectCertainty != toolcontract.EffectCompleted {
				t.Fatalf("an HTTP error status is a completed exchange: %+v", r.result)
			}
		})
	}
	h.write("dest.txt", "old")
	if r := h.curl(t.Context(), "-s", "--fail-with-body", "-o", "dest.txt", server.URL+"/broken"); r.result.Status != 22 || h.read("dest.txt") != "stack trace here\n" {
		t.Fatalf("--fail-with-body -o kept %q: %+v", h.read("dest.txt"), r.result)
	}
	for _, bad := range [][]string{{"-f", "--fail-with-body"}, {"-r", "abc"}, {"-r", "-"}, {"-r", ""}} {
		if reject := toolrejection.AsToolReject(h.curl(t.Context(), append(bad, server.URL)...).err); reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" {
			t.Fatalf("%v accepted", bad)
		}
	}
	for _, refused := range [][]string{{"--proxy", "http://proxy.test:3128"}, {"-x", "http://proxy.test:3128"}, {"--noproxy", "*"}, {"--retry", "3"}} {
		if reject := toolrejection.AsToolReject(h.curl(t.Context(), append(refused, server.URL)...).err); reject == nil || reject.Code != "COMMAND_OPTION_UNSUPPORTED" {
			t.Fatalf("%v was not refused as unsupported", refused)
		}
	}
}

func TestCurlWriteOutVariables(t *testing.T) {
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moved":
			stamp(w, "text/plain")
			w.Header().Set("Location", "/target?x=1")
			w.WriteHeader(http.StatusFound)
		default:
			_, _ = io.ReadAll(r.Body)
			stamp(w, "application/json; charset=utf-8")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	})
	host := strings.TrimPrefix(server.URL, "http://")
	port := host[strings.LastIndexByte(host, ':')+1:]
	h := newHarness(t)
	format := `%{http_code}|%{http_version}|%{num_redirects}|%{redirect_url}|%{content_type}|%{remote_ip}|%{remote_port}|%{size_upload}|%{size_header}|%{exitcode}`
	finalHeaders := "HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nDate: Tue, 20 Oct 2026 12:00:00 GMT\r\nContent-Length: 11\r\n\r\n"
	redirectHeaders := "HTTP/1.1 302 Found\r\nContent-Type: text/plain\r\nDate: Tue, 20 Oct 2026 12:00:00 GMT\r\nLocation: /target?x=1\r\nContent-Length: 0\r\n\r\n"
	cases := []struct {
		name, want string
		args       []string
	}{
		{"not followed", fmt.Sprintf("302|1.1|0|%s/target?x=1|text/plain|127.0.0.1|%s|0|%d|0", server.URL, port, len(redirectHeaders)), []string{server.URL + "/moved"}},
		{"followed", fmt.Sprintf("200|1.1|1||application/json; charset=utf-8|127.0.0.1|%s|5|%d|0", port, len(redirectHeaders)+len(finalHeaders)), []string{"-L", "-d", "a=b&c", server.URL + "/moved"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.curl(t.Context(), append([]string{"-s", "-o", "/dev/null", "-w", format}, c.args...)...)
			if r.err != nil || r.stdout != c.want {
				t.Fatalf("write-out %q, want %q (%v)", r.stdout, c.want, r.err)
			}
		})
	}
	r := h.curl(t.Context(), "-s", "-o", "/dev/null", "-w", "%{time_namelookup} %{time_connect} %{time_appconnect} %{time_starttransfer} %{time_total} %{speed_download}", server.URL)
	fields := strings.Fields(r.stdout)
	if r.err != nil || len(fields) != 6 || fields[2] != "0.000000" {
		t.Fatalf("timing write-out %q %v", r.stdout, r.err)
	}
	var previous float64
	for _, i := range []int{0, 1, 3, 4} {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || value < previous || len(strings.Split(fields[i], ".")[1]) != 6 {
			t.Fatalf("phase %d out of order or format in %q", i, r.stdout)
		}
		previous = value
	}
	if _, err := strconv.ParseInt(fields[5], 10, 64); err != nil {
		t.Fatalf("speed_download is not an integer: %q", r.stdout)
	}
	closed := closedURL(t)
	r = h.curl(t.Context(), "-s", "-w", "%{http_code}|%{exitcode}|%{remote_ip}|%{num_redirects}|%{http_version}|%{time_connect}", closed)
	if r.result.Status != 7 || r.stdout != "000|7||0|0|0.000000" {
		t.Fatalf("failed transfer write-out %q status %d", r.stdout, r.result.Status)
	}
	if reject := toolrejection.AsToolReject(h.curl(t.Context(), "-w", "%{certs}", server.URL).err); reject == nil || reject.Code != "COMMAND_OPERAND_INVALID" {
		t.Fatal("unsupported -w variable accepted")
	}
}
