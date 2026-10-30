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

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *commandProgramSession) outcome(t *testing.T, callID string) command.ProgramOutcome {
	t.Helper()
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "get messages", err)
	for _, raw := range msgs {
		msg := messageview.RedactMessage(raw)
		if msg.Role != wire.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.ToolCallID != callID {
			continue
		}
		_, _, err := messageview.RetainedContent(raw, "tool_output", callID)
		testutil.FailErr(t, "retained content "+callID, err)
		_, jsonBody, _, ok := hostmarker.SplitToolJSONBody(raw.ToolResult.Content)
		if !ok {
			t.Fatalf("call %s tool result has no json body: %q", callID, raw.ToolResult.Content)
		}
		var out command.ProgramOutcome
		testutil.FailErr(t, "unmarshal outcome "+callID, json.Unmarshal([]byte(jsonBody), &out))
		if out.ProgramID == "" {
			t.Fatalf("call %s missing program_id in %s", callID, jsonBody)
		}
		return out
	}
	t.Fatalf("no tool result found for call %s", callID)
	return command.ProgramOutcome{}
}

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
	}{
		{"pipeline", "curl -s '" + server.URL + "' | sha256sum"},
		{"file", "curl -s -o downloaded.txt '" + server.URL + "' && cmp downloaded.txt expected.txt"},
		{"model", "curl -s '" + server.URL + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCommandProgramSession(t, tc.source, "")
			if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-listener", "curl-fixture", nil) {
				t.Fatal("listener grant failed")
			}
			testutil.FailErr(t, "seed expected", os.WriteFile(filepath.Join(s.dir, "expected.txt"), []byte(body), 0600))
			testutil.FailErr(t, "run command", s.prompt(t.Context()))
			result := s.outcome(t, "program-1")
			delivered := s.result(t)
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
				if strings.Contains(delivered, "AKIAQYJK5TXV4NZR7SGB") || strings.Contains(delivered, "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3") || !strings.Contains(delivered, secretmatch.RedactedMarker) {
					t.Fatalf("unscreened model output: %q", delivered)
				}
			}
		})
	}
}
