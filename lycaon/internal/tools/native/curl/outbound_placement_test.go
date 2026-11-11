package curl

import (
	"context"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A protected value withheld from a recipient never reaches it, wherever curl
// puts it in the request; the same placements deliver it exactly once approved.
func TestCurlScreensAProtectedValueInEveryOutboundPlacement(t *testing.T) {
	const secret = "protected-placement-value-4471"
	var mu sync.Mutex
	var received []string
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		dump, _ := httputil.DumpRequest(r, true)
		if _, password, ok := r.BasicAuth(); ok {
			dump = append(dump, "\nbasic password: "+password...)
		}
		mu.Lock()
		received = append(received, string(dump))
		mu.Unlock()
		stamp(w, "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	cases := []struct {
		name  string
		stdin string
		files map[string]string
		words []string
	}{
		{name: "header", words: []string{"-H", "X-Token: " + secret}},
		{name: "query", words: []string{server.URL + "/q?token=" + secret}},
		{name: "get_data", words: []string{"-G", "-d", "token=" + secret}},
		{name: "auth", words: []string{"-u", "deploy:" + secret}},
		{name: "data_file", files: map[string]string{"payload.txt": "token=" + secret}, words: []string{"--data-binary", "@payload.txt"}},
		{name: "stdin", stdin: "token=" + secret, words: []string{"--data-binary", "@-"}},
		{name: "upload", files: map[string]string{"upload.bin": "line\n" + secret + "\n"}, words: []string{"-T", "upload.bin"}},
		{name: "multipart_file", files: map[string]string{"part.txt": secret}, words: []string{"-F", "file=@part.txt"}},
		{name: "multipart_field", words: []string{"-F", "token=" + secret}},
		{name: "cookie_jar", files: map[string]string{"jar.txt": "127.0.0.1\tFALSE\t/\tFALSE\t0\tsid\t" + secret + "\n"}, words: []string{"-b", "jar.txt"}},
	}
	for _, approve := range []bool{false, true} {
		decision := secretmatch.Withhold
		if approve {
			decision = secretmatch.SendUnchanged
		}
		for _, c := range cases {
			t.Run(string(decision)+"/"+c.name, func(t *testing.T) {
				h := newHarness(t)
				matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
				testutil.FailErr(t, "build matcher", err)
				fp, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("p", 32)))
				testutil.FailErr(t, "build fingerprinter", err)
				matcher.SetFingerprinter(fp)
				matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
					return []secretmatch.HarvestedValue{{Name: "PLACED", Secret: secret, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}}
				})
				h.owners.HTTP.SecretMatcher = matcher
				asks := 0
				h.owners.HTTP.SecretAsk = func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
					asks++
					return secretmatch.Resolution{Decision: decision}, nil
				}
				for name, content := range c.files {
					h.write(name, content)
				}
				words := append([]string{"-s"}, c.words...)
				if !strings.HasPrefix(c.words[len(c.words)-1], "http") {
					words = append(words, server.URL+"/")
				}
				mu.Lock()
				received = nil
				mu.Unlock()
				r := h.curlStdin(t.Context(), c.stdin, words...)
				mu.Lock()
				got := strings.Join(received, "\n")
				mu.Unlock()
				if asks != 1 {
					t.Fatalf("asked %d times, want once (result %+v err %v)", asks, r.result, r.err)
				}
				if approve {
					if r.err != nil || len(received) != 1 || !strings.Contains(got, secret) {
						t.Fatalf("approved %s: err %v, received %q", c.name, r.err, got)
					}
					return
				}
				if strings.Contains(got, secret) {
					t.Fatalf("withheld %s reached the recipient: %q", c.name, got)
				}
			})
		}
	}
}
