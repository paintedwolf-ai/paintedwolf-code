package wiring

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestCommandProgramCurlPreservesDataAndScreensModelDelivery(t *testing.T) {
	body := "aws=AKIAQYJK5TXV4NZR7SGB\ngithub=ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
	defer server.Close()
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse listener", err)
	port, err := strconv.ParseUint(target.Port(), 10, 16)
	testutil.FailErr(t, "listener port", err)
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name, source string
		screened     bool
	}{
		{"pipeline", "curl -s '" + server.URL + "' | sha256sum", false},
		{"file", "curl -s -o downloaded.txt '" + server.URL + "' && cmp downloaded.txt expected.txt", false},
		{"model", "curl -s '" + server.URL + "'", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCommandProgramSession(t, tc.source, "")
			if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-listener", "curl-fixture", time.Now().Add(time.Minute)) {
				t.Fatal("listener grant failed")
			}
			testutil.FailErr(t, "seed expected", os.WriteFile(filepath.Join(s.dir, "expected.txt"), []byte(body), 0600))
			testutil.FailErr(t, "run command", s.prompt(t.Context()))
			var result command.ProgramOutcome
			testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(s.result(t)), &result))
			if result.Status != 0 || result.State != "completed" {
				t.Fatalf("command failed: %+v", result)
			}
			requireExecutions(t, s.executions(t), "http_request=completed")
			switch tc.name {
			case "pipeline":
				if strings.TrimSpace(result.Stdout) != digest+"  -" {
					t.Fatalf("pipeline hashed altered bytes: %q", result.Stdout)
				}
			case "file":
				data, err := os.ReadFile(filepath.Join(s.dir, "downloaded.txt"))
				testutil.FailErr(t, "read downloaded response", err)
				if string(data) != body {
					t.Fatalf("file changed: %q", data)
				}
			case "model":
				if strings.Contains(result.Stdout, "AKIAQYJK5TXV4NZR7SGB") || strings.Contains(result.Stdout, "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3") || !strings.Contains(result.Stdout, secretmatch.RedactedMarker) {
					t.Fatalf("unscreened model output: %q", result.Stdout)
				}
			}
		})
	}
}
