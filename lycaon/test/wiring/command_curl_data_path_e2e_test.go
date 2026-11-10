package wiring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm"
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

func TestCommandProgramCurlResultIngestionAndFileCustody(t *testing.T) {
	body := "external-payload-line-1\nexternal-payload-line-2\n"
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/fail") {
			w.Header().Set("X-Remote-Error", "upstream-failed")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "upstream failure body\n")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/report") {
			_, _ = io.WriteString(w, "sink-ok\n")
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer proxyServer.Close()

	target, err := url.Parse(proxyServer.URL)
	testutil.FailErr(t, "parse proxy listener", err)
	portStr := target.Port()
	port, err := strconv.ParseUint(portStr, 10, 16)
	testutil.FailErr(t, "proxy port", err)

	resolveAPI := "api.external.example:" + portStr + ":127.0.0.1"
	apiBase := "http://api.external.example:" + portStr
	resolveSink := "sink.external.example:" + portStr + ":127.0.0.1"
	sinkBase := "http://sink.external.example:" + portStr

	approvals := "  - category: tool\n    pattern: http_request\n    effect: allow\n  - category: network_recipient\n    pattern: '*'\n    effect: allow\n"

	for _, tc := range []struct {
		name          string
		source        string
		wantUntrusted bool
		wantURL       string
	}{
		{
			name:          "external_stdout_marks_untrusted",
			source:        "curl -s --resolve '" + resolveAPI + "' '" + apiBase + "/v1/items'",
			wantUntrusted: true,
			wantURL:       apiBase + "/v1/items",
		},
		{
			name:          "external_pipeline_to_stdout_marks_untrusted",
			source:        "curl -s --resolve '" + resolveAPI + "' '" + apiBase + "/v1/items' | head -n 1",
			wantUntrusted: true,
			wantURL:       apiBase + "/v1/items",
		},
		{
			name:          "external_partial_failure_after_headers_marks_untrusted",
			source:        "curl -s --resolve '" + resolveAPI + "' --fail -D - '" + apiBase + "/v1/fail'",
			wantUntrusted: true,
			wantURL:       apiBase + "/v1/fail",
		},
		{
			name:          "external_dash_o_file_only_does_not_mark_model_ingestion",
			source:        "curl -s --resolve '" + resolveAPI + "' -o downloaded.txt '" + apiBase + "/v1/items' && cmp downloaded.txt expected.txt",
			wantUntrusted: false,
		},
		{
			name:          "external_shell_redirect_file_only_does_not_mark_model_ingestion",
			source:        "curl -s --resolve '" + resolveAPI + "' '" + apiBase + "/v1/items' > redirected.txt",
			wantUntrusted: false,
		},
		{
			name:          "loopback_curl_stays_clean",
			source:        "curl -s '" + proxyServer.URL + "/local'",
			wantUntrusted: false,
		},
		{
			name:          "pure_local_echo_stays_clean",
			source:        "echo local-only",
			wantUntrusted: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCommandProgramSession(t, tc.source, approvals)
			if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-proxy", "curl-fixture", nil) {
				t.Fatal("listener grant failed")
			}
			testutil.FailErr(t, "seed expected", os.WriteFile(filepath.Join(s.dir, "expected.txt"), []byte(body), 0o600))
			testutil.FailErr(t, "run command", s.prompt(t.Context()))

			untrusted, err := s.h.Sessions.Store.SessionUntrustedContentResult(t.Context(), s.session)
			testutil.FailErr(t, "SessionUntrustedContentResult", err)
			if untrusted != tc.wantUntrusted {
				t.Fatalf("SessionUntrustedContentResult = %v, want %v", untrusted, tc.wantUntrusted)
			}
			if tc.wantURL != "" {
				ev, err := s.h.Sessions.Store.LoadLedger(t.Context(), s.session)
				testutil.FailErr(t, "LoadLedger", err)
				urls := evidence.ObservedURLsSorted(ev)
				if !strings.Contains(strings.Join(urls, ","), tc.wantURL) {
					t.Fatalf("ObservedURLsSorted = %v, want %q", urls, tc.wantURL)
				}
			}
		})
	}

	t.Run("subsequent_cat_of_external_custody_file_marks_untrusted", func(t *testing.T) {
		mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
			{
				Pattern:      "step 1 download",
				ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": "curl -s --resolve '" + resolveAPI + "' -o saved.txt '" + apiBase + "/v1/saved'"}}},
				FollowUpText: "Downloaded.",
			},
			{
				Pattern:      "step 2 inspect",
				ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": "cat saved.txt"}}},
				FollowUpText: "Read.",
			},
		}})
		approvalsWithWrite := approvals + "  - category: file\n    pattern: '*'\n    effect: allow\n"
		s := startSessionWithClient(t, mock, wire.CreateSessionRequest{}, approvalsWithWrite)
		s.h.SeedProgress(t, t.Context(), s.session)
		if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-proxy", "curl-fixture", nil) {
			t.Fatal("listener grant failed")
		}

		_, err := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 1 download")
		testutil.FailErr(t, "step 1 download", err)

		untrustedAfterDownload, err := s.h.Sessions.Store.SessionUntrustedContentResult(t.Context(), s.session)
		testutil.FailErr(t, "SessionUntrustedContentResult after download", err)
		if untrustedAfterDownload {
			t.Fatal("file-only download must not mark model ingestion before file is read")
		}

		_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 2 inspect")
		testutil.FailErr(t, "step 2 inspect", err)

		untrustedAfterRead, err := s.h.Sessions.Store.SessionUntrustedContentResult(t.Context(), s.session)
		testutil.FailErr(t, "SessionUntrustedContentResult after cat", err)
		if !untrustedAfterRead {
			t.Fatal("subsequent cat of external custody file must mark untrusted ingestion")
		}

		ev, err := s.h.Sessions.Store.LoadLedger(t.Context(), s.session)
		testutil.FailErr(t, "LoadLedger", err)
		urls := evidence.ObservedURLsSorted(ev)
		if !strings.Contains(strings.Join(urls, ","), apiBase+"/v1/saved") {
			t.Fatalf("ObservedURLsSorted after cat = %v, want %s/v1/saved", urls, apiBase)
		}
	})

	t.Run("custody_supersession_partial_edit_vs_full_overwrite", func(t *testing.T) {
		mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
			{
				Pattern:      "step 1 download",
				ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": "curl -s --resolve '" + resolveAPI + "' -o saved.txt '" + apiBase + "/v1/saved'"}}},
				FollowUpText: "Downloaded.",
			},
			{
				Pattern:      "step 2 partial sed",
				ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": "sed -i 's/line-1/line-one/' saved.txt"}}},
				FollowUpText: "Edited.",
			},
			{
				Pattern:      "step 3 full overwrite",
				ToolCalls:    []llm.MockToolCall{{ID: "program-3", Name: "command", Args: map[string]any{"command": "echo 'clean local replacement' > saved.txt"}}},
				FollowUpText: "Overwritten.",
			},
			{
				Pattern:      "step 4 cat after overwrite",
				ToolCalls:    []llm.MockToolCall{{ID: "program-4", Name: "command", Args: map[string]any{"command": "cat saved.txt"}}},
				FollowUpText: "Read overwritten.",
			},
		}})
		approvalsWithWrite := approvals + "  - category: file\n    pattern: '*'\n    effect: allow\n"
		s := startSessionWithClient(t, mock, wire.CreateSessionRequest{}, approvalsWithWrite)
		s.h.SeedProgress(t, t.Context(), s.session)
		if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-proxy", "curl-fixture", nil) {
			t.Fatal("listener grant failed")
		}

		_, err := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 1 download")
		testutil.FailErr(t, "step 1 download", err)
		_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 2 partial sed")
		testutil.FailErr(t, "step 2 partial sed", err)
		evAfterPartial, err := s.h.Sessions.Store.LoadLedger(t.Context(), s.session)
		testutil.FailErr(t, "LoadLedger after sed -i", err)
		custodyAfterPartial := evidence.ExternalCustodyURLs(evAfterPartial, []string{"saved.txt"})
		if !slices.Contains(custodyAfterPartial, apiBase+"/v1/saved") {
			t.Fatalf("partial edit (sed -i) must preserve external custody URLs, got %v", custodyAfterPartial)
		}

		_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 3 full overwrite")
		testutil.FailErr(t, "step 3 full overwrite", err)
		evAfterOverwrite, err := s.h.Sessions.Store.LoadLedger(t.Context(), s.session)
		testutil.FailErr(t, "LoadLedger after > overwrite", err)
		custodyAfterOverwrite := evidence.ExternalCustodyURLs(evAfterOverwrite, []string{"saved.txt"})
		if len(custodyAfterOverwrite) != 0 {
			t.Fatalf("full local overwrite (>) must supersede older external custody, got %v", custodyAfterOverwrite)
		}

		_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 4 cat after overwrite")
		testutil.FailErr(t, "step 4 cat after overwrite", err)
		untrustedAfterCleanRead, err := s.h.Sessions.Store.SessionUntrustedContentResult(t.Context(), s.session)
		testutil.FailErr(t, "SessionUntrustedContentResult after cat of overwritten file", err)
		if untrustedAfterCleanRead {
			t.Fatal("cat of file after full local overwrite must not mark untrusted ingestion")
		}
	})

	t.Run("downstream_egress_policy_contrast_after_curl_ingestion", func(t *testing.T) {
		balancedRules := "  - category: tool\n    pattern: http_request\n    effect: allow\n"

		// 1. Clean local session: echo local -> outbound curl to sink.external.example short-circuits under Balanced/Observe without calling the egress resolver.
		cleanMock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
			{
				Pattern:      "clean step 1",
				ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": "echo local-only"}}},
				FollowUpText: "Local.",
			},
			{
				Pattern:      "clean step 2",
				ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": "curl -s --resolve '" + resolveSink + "' '" + sinkBase + "/report'"}}},
				FollowUpText: "Sent.",
			},
		}})
		cleanSess := startSessionWithClient(t, cleanMock, wire.CreateSessionRequest{}, balancedRules, gate.PostureBalanced)
		cleanSess.h.SeedProgress(t, t.Context(), cleanSess.session)
		if !cleanSess.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(cleanSess.session, []uint16{uint16(port)}, "curl-proxy", "curl-fixture", nil) {
			t.Fatal("listener grant failed")
		}
		var cleanResolverHosts []string
		releaseClean := confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			cleanResolverHosts = append(cleanResolverHosts, ep.Host)
			return false
		})
		defer func() { _ = releaseClean(context.Background()) }()

		_, err := cleanSess.h.Sessions.Manager.Submissions.Prompt(t.Context(), cleanSess.session, "clean step 1")
		testutil.FailErr(t, "clean step 1", err)
		_, err = cleanSess.h.Sessions.Manager.Submissions.Prompt(t.Context(), cleanSess.session, "clean step 2")
		testutil.FailErr(t, "clean step 2", err)
		if out := cleanSess.outcome(t, "program-2"); out.Status != 0 || out.Refusal != nil {
			t.Fatalf("expected clean Balanced session to allow outbound request without resolver intervention, got %+v (resolverHosts=%v)", out, cleanResolverHosts)
		}
		if len(cleanResolverHosts) != 0 {
			t.Fatalf("clean Balanced session must not invoke egress resolver, got %v", cleanResolverHosts)
		}

		// 2. Ingested session: curl external body -> outbound curl to sink.external.example stops short-circuiting and routes through the egress resolver (denied when rejected, allowed on recovery when approved).
		ingestedMock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
			{
				Pattern:      "ingested step 1",
				ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": "curl -s --resolve '" + resolveAPI + "' '" + apiBase + "/v1/items'"}}},
				FollowUpText: "Ingested.",
			},
			{
				Pattern:      "ingested step 2 deny",
				ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": "curl -s --resolve '" + resolveSink + "' '" + sinkBase + "/report'"}}},
				FollowUpText: "Blocked.",
			},
			{
				Pattern:      "ingested step 3 approve",
				ToolCalls:    []llm.MockToolCall{{ID: "program-3", Name: "command", Args: map[string]any{"command": "curl -s --resolve '" + resolveSink + "' '" + sinkBase + "/report'"}}},
				FollowUpText: "Recovered.",
			},
		}})
		ingestedSess := startSessionWithClient(t, ingestedMock, wire.CreateSessionRequest{}, balancedRules, gate.PostureBalanced)
		ingestedSess.h.SeedProgress(t, t.Context(), ingestedSess.session)
		if !ingestedSess.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(ingestedSess.session, []uint16{uint16(port)}, "curl-proxy", "curl-fixture", nil) {
			t.Fatal("listener grant failed")
		}
		allowSink := false
		var ingestedResolverHosts []string
		releaseIngested := confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			ingestedResolverHosts = append(ingestedResolverHosts, ep.Host)
			if ep.Host == "sink.external.example" {
				return allowSink
			}
			return true
		})
		defer func() { _ = releaseIngested(context.Background()) }()

		_, err = ingestedSess.h.Sessions.Manager.Submissions.Prompt(t.Context(), ingestedSess.session, "ingested step 1")
		testutil.FailErr(t, "ingested step 1", err)
		_, err = ingestedSess.h.Sessions.Manager.Submissions.Prompt(t.Context(), ingestedSess.session, "ingested step 2 deny")
		testutil.FailErr(t, "ingested step 2 deny", err)
		if out := ingestedSess.outcome(t, "program-2"); out.Refusal == nil {
			t.Fatalf("expected ingested Balanced session to refuse denied sink.external.example, got %+v", out)
		}
		if !slices.Contains(ingestedResolverHosts, "sink.external.example") {
			t.Fatalf("expected ingested Balanced session to invoke egress resolver for sink.external.example, got %v", ingestedResolverHosts)
		}

		// 3. Positive authorized outbound / recovery path: once authorized, retry to sink.external.example succeeds.
		allowSink = true
		_, err = ingestedSess.h.Sessions.Manager.Submissions.Prompt(t.Context(), ingestedSess.session, "ingested step 3 approve")
		testutil.FailErr(t, "ingested step 3 approve", err)
		if out := ingestedSess.outcome(t, "program-3"); out.Status != 0 || out.Refusal != nil || !strings.Contains(out.Stdout, "sink-ok") {
			t.Fatalf("expected authorized recovery request to sink.external.example to succeed, got %+v", out)
		}
	})
}

