package wiring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/projectignore"
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

// promptAnsweringCards submits one prompt and answers the approval cards it
// raises, in order, with options ("decline" refuses). A card beyond them fails.
func promptAnsweringCards(t *testing.T, s *commandProgramSession, prompt string, options ...string) []wire.CheckpointEvent {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, prompt)
		done <- err
	}()
	kind := wire.CheckpointKindToolApproval
	var cards []wire.CheckpointEvent
	answered := map[string]bool{}
	var runErr error
	testutil.WaitFor(t, func() bool {
		select {
		case runErr = <-done:
			return true
		default:
		}
		pending, err := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
		if err != nil {
			return false
		}
		for _, card := range pending {
			if answered[card.ID] {
				continue
			}
			answered[card.ID] = true
			if len(cards) >= len(options) {
				t.Fatalf("%s: unexpected card: %s", prompt, cardText(t, card))
			}
			cards = append(cards, card)
			answerCard(t, s, card, options[len(cards)-1])
		}
		return false
	})
	testutil.FailErr(t, prompt, runErr)
	if len(cards) != len(options) {
		t.Fatalf("%s: %d cards, want %d", prompt, len(cards), len(options))
	}
	return cards
}

func answerCard(t *testing.T, s *commandProgramSession, card wire.CheckpointEvent, option string) {
	t.Helper()
	owner := s.h.OwnerCtx(t, t.Context())
	if option == "decline" {
		_, err := s.h.Sessions.Checkpoints.ResolveCheckpoint(owner, s.session, card.ID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
		testutil.FailErr(t, "decline card", err)
		return
	}
	_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(owner, s.session, card.ID, option)
	testutil.FailErr(t, "answer card", err)
}

func cardText(t *testing.T, card wire.CheckpointEvent) string {
	t.Helper()
	encoded, err := json.Marshal(card)
	testutil.FailErr(t, "marshal card", err)
	return string(encoded)
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

		// 1. Clean local session: the first contact with sink.external.example is
		// audited, not asked, at Balanced.
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
		promptAnsweringCards(t, cleanSess, "clean step 1")
		if cards := promptAnsweringCards(t, cleanSess, "clean step 2"); len(cards) != 0 {
			t.Fatalf("a clean Balanced chat asked about its first contact: %+v", cards[0].ToolApproval.Plan.Subject)
		}
		if out := cleanSess.outcome(t, "program-2"); out.Status != 0 || out.Refusal != nil {
			t.Fatalf("clean Balanced first contact = %+v", out)
		}

		// 2. Ingested session: after reading external content the same first
		// contact asks; declining refuses it and approving recovers.
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
		if cards := promptAnsweringCards(t, ingestedSess, "ingested step 1"); len(cards) != 0 {
			t.Fatalf("the clean chat's first fetch asked: %+v", cards[0].ToolApproval.Plan.Subject)
		}
		cards := promptAnsweringCards(t, ingestedSess, "ingested step 2 deny", "decline")
		if !strings.Contains(cardText(t, cards[0]), "sink.external.example") {
			t.Fatalf("ingested chat's card does not name the sink: %s", cardText(t, cards[0]))
		}
		if out := ingestedSess.outcome(t, "program-2"); out.Refusal == nil {
			t.Fatalf("declined contact after ingestion was not refused: %+v", out)
		}

		// 3. Approving the contact recovers the request.
		promptAnsweringCards(t, ingestedSess, "ingested step 3 approve", "approve_current_action")
		if out := ingestedSess.outcome(t, "program-3"); out.Status != 0 || out.Refusal != nil || !strings.Contains(out.Stdout, "sink-ok") {
			t.Fatalf("expected authorized recovery request to sink.external.example to succeed, got %+v", out)
		}
	})
}

func TestCommandProgramCurlRedirectDenialUnknownHopAndRecoveryRealAdapter(t *testing.T) {
	var originHits, targetHits atomic.Int32
	dropHop1AfterRequest := atomic.Bool{}

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/seed":
			_, _ = io.WriteString(w, "external-seed\n")
		case strings.HasPrefix(r.Host, "origin.redirect.example"):
			originHits.Add(1)
			http.Redirect(w, r, "http://"+r.URL.Query().Get("target")+"/landed", http.StatusFound)
		default:
			targetHits.Add(1)
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
			_, _ = io.WriteString(w, "landed-real-adapter\n")
		}
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	testutil.FailErr(t, "parse proxy listener", err)
	portStr := proxyURL.Port()
	port, err := strconv.ParseUint(portStr, 10, 16)
	testutil.FailErr(t, "proxy port", err)
	originHost := "origin.redirect.example:" + portStr
	targetHost := "target.redirect.example:" + portStr
	originResolve := originHost + ":127.0.0.1"
	targetResolve := targetHost + ":127.0.0.1"

	var mu sync.Mutex
	var asked []string
	allow := map[string]bool{
		"127.0.0.1:" + portStr: true,
		originHost:             true,
	}
	// fixtureSessionGrants simulates per-session grant reuse inside the test resolver;
	// durable persistent grant store lifecycle is qualified separately.
	fixtureSessionGrants := map[string]bool{}
	cmdText := "curl -s -L --resolve '" + originResolve + "' --resolve '" + targetResolve + "' 'http://" + originHost + "/start?target=" + targetHost + "'"
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern:      "step 1 deny hop 1",
			ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": cmdText}}},
			FollowUpText: "Denied hop 1.",
		},
		{
			Pattern:      "step 2 unknown hop 1",
			ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": cmdText}}},
			FollowUpText: "Unknown hop 1.",
		},
		{
			Pattern:      "step 3 authorized recovery",
			ToolCalls:    []llm.MockToolCall{{ID: "program-3", Name: "command", Args: map[string]any{"command": cmdText}}},
			FollowUpText: "Recovered.",
		},
		{
			Pattern:      "step 4 quiet repeat",
			ToolCalls:    []llm.MockToolCall{{ID: "program-4", Name: "command", Args: map[string]any{"command": cmdText}}},
			FollowUpText: "Repeated.",
		},
	}})

	approvals := "  - category: tool\n    pattern: http_request\n    effect: allow\n"
	s := startSessionWithClient(t, mock, wire.CreateSessionRequest{}, approvals, gate.PostureBalanced)
	s.h.SeedProgress(t, t.Context(), s.session)
	if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-redirect-real", "curl-fixture", nil) {
		t.Fatal("listener grant failed")
	}

	// wireEgressPolicy in startSessionWithClient installs an EgressPostureResolver and EgressApprovals
	// resolver for the harness; override both after session creation so this test exercises per-hop
	// confine.PostureAsk admission directly through the real native curl adapter.
	confine.SetApprovalsDisabledSource(func(confine.EgressCommand) bool { return false })
	confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return confine.PostureAsk })
	release := confine.SetEgressResolver(func(_ context.Context, cmd confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		mu.Lock()
		defer mu.Unlock()
		endpoint := net.JoinHostPort(ep.Host, strconv.Itoa(int(ep.Port)))
		grantKey := cmd.SessionID + "|" + endpoint
		if fixtureSessionGrants[grantKey] {
			return true
		}
		asked = append(asked, endpoint)
		if allow[endpoint] {
			fixtureSessionGrants[grantKey] = true
			return true
		}
		return false
	})
	defer func() {
		confine.SetEgressPostureResolver(nil)
		confine.SetApprovalsDisabledSource(nil)
		_ = release(context.Background())
	}()

	// 1. Redirect denial after completed Hop 0 through the real registered native curl adapter and command runtime.
	_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 1 deny hop 1")
	testutil.FailErr(t, "step 1 deny hop 1", err)
	outDeny := s.outcome(t, "program-1")
	if outDeny.State != "rejected" || outDeny.CompletedEffects != 1 || outDeny.ReplayAvailable || outDeny.Refusal == nil || outDeny.Refusal.Code != "HTTP_REQUEST_HOST_DENIED" {
		t.Fatalf("step 1 real adapter denied redirect hop outcome = %+v / refusal=%+v", outDeny, outDeny.Refusal)
	}
	if originHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("step 1 hits: origin=%d target=%d, want 1/0", originHits.Load(), targetHits.Load())
	}
	details := outDeny.Refusal.Details
	if details["command_execution"] != "observed" || details["program_completed_effects"] != float64(1) || details["program_replay_available"] != false ||
		details["http_requests_started"] != float64(1) || details["http_responses_received"] != float64(1) {
		t.Fatalf("step 1 refusal details lost completed initial hop state: %#v", details)
	}
	if !strings.Contains(outDeny.Refusal.Feedback, "Earlier exchange attempts have already started (1 attempts, 1 responses).") ||
		strings.Contains(outDeny.Refusal.Feedback, "No command operation ran") {
		t.Fatalf("step 1 refusal feedback misrepresented completed initial hop: %q", outDeny.Refusal.Feedback)
	}

	// An unknown second-hop outcome stays uncertain and is never replayed.
	// UncertainEffects==1 and ReplayAvailable==false, without auto-replaying Hop 0 or Hop 1.
	originHits.Store(0)
	targetHits.Store(0)
	dropHop1AfterRequest.Store(true)
	mu.Lock()
	allow[targetHost] = true
	asked = nil
	fixtureSessionGrants = map[string]bool{}
	mu.Unlock()

	_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 2 unknown hop 1")
	testutil.FailErr(t, "step 2 unknown hop 1", err)
	outUnknown := s.outcome(t, "program-2")
	if outUnknown.UncertainEffects != 1 || outUnknown.ReplayAvailable || originHits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("step 2 real adapter unknown Hop 1 outcome = %+v originHits=%d targetHits=%d, want UncertainEffects=1 ReplayAvailable=false and 1/1 hits (no auto-replay)",
			outUnknown, originHits.Load(), targetHits.Load())
	}

	// An explicit retry succeeds quietly once the second hop is healthy.
	// the fixture session grant resolver.
	originHits.Store(0)
	targetHits.Store(0)
	dropHop1AfterRequest.Store(false)
	mu.Lock()
	asked = nil
	fixtureSessionGrants = map[string]bool{}
	mu.Unlock()

	_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 3 authorized recovery")
	testutil.FailErr(t, "step 3 authorized recovery", err)
	outRecover := s.outcome(t, "program-3")
	if outRecover.State != "completed" || outRecover.Status != 0 || outRecover.CompletedEffects != 1 || strings.TrimSpace(outRecover.Stdout) != "landed-real-adapter" {
		t.Fatalf("step 3 real adapter authorized recovery outcome = %+v", outRecover)
	}
	if originHits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("step 3 hits: origin=%d target=%d, want 1/1", originHits.Load(), targetHits.Load())
	}
	mu.Lock()
	askedFirst := append([]string(nil), asked...)
	asked = nil
	mu.Unlock()
	if !slices.Contains(askedFirst, targetHost) {
		t.Fatalf("step 3 asked = %v, want to include %s", askedFirst, targetHost)
	}

	_, err = s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 4 quiet repeat")
	testutil.FailErr(t, "step 4 quiet repeat", err)
	outRepeat := s.outcome(t, "program-4")
	if outRepeat.State != "completed" || outRepeat.Status != 0 || strings.TrimSpace(outRepeat.Stdout) != "landed-real-adapter" {
		t.Fatalf("step 4 real adapter quiet repeat outcome = %+v", outRepeat)
	}
	mu.Lock()
	askedSecond := append([]string(nil), asked...)
	mu.Unlock()
	if len(askedSecond) != 0 {
		t.Fatalf("step 4 quiet repeat re-prompted = %v, want empty", askedSecond)
	}
}

func TestCommandProgramCurlCookieJarSecretAuthorityAndPersistentGrantRealAdapter(t *testing.T) {
	const cookieSecret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	const unrelatedSecret = "AKIAQYJK5TXV4NZR7SGB"

	var originHits, targetHits atomic.Int32
	var lastOriginCookie, lastTargetCookie atomic.Value
	lastOriginCookie.Store("")
	lastTargetCookie.Store("")
	var revokeTargetOnRedirect atomic.Bool
	var onRedirectRevoke func()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.Host, "origin.cookie.example") && r.URL.Path == "/redirect":
			originHits.Add(1)
			lastOriginCookie.Store(r.Header.Get("Cookie"))
			if revokeTargetOnRedirect.Load() && onRedirectRevoke != nil {
				onRedirectRevoke()
			}
			http.Redirect(w, r, "http://"+r.URL.Query().Get("target")+"/landed", http.StatusFound)
		case strings.HasPrefix(r.Host, "origin.cookie.example"):
			originHits.Add(1)
			lastOriginCookie.Store(r.Header.Get("Cookie"))
			_, _ = io.WriteString(w, "origin-cookie-ok\n")
		default:
			targetHits.Add(1)
			lastTargetCookie.Store(r.Header.Get("Cookie"))
			_, _ = io.WriteString(w, "target-cookie-ok\n")
		}
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	testutil.FailErr(t, "parse proxy listener", err)
	portStr := proxyURL.Port()
	port, err := strconv.ParseUint(portStr, 10, 16)
	testutil.FailErr(t, "proxy port", err)
	originHost := "origin.cookie.example:" + portStr
	targetHost := "target.cookie.example:" + portStr
	originResolve := originHost + ":127.0.0.1"
	targetResolve := targetHost + ":127.0.0.1"

	cmdOrigin := "curl -s -b cookies.txt --resolve '" + originResolve + "' 'http://" + originHost + "/ok'"
	cmdTarget := "curl -s -b cookies.txt --resolve '" + targetResolve + "' 'http://" + targetHost + "/landed'"
	cmdRedirect := "curl -s -L -b cookies.txt --resolve '" + originResolve + "' --resolve '" + targetResolve + "' 'http://" + originHost + "/redirect?target=" + targetHost + "'"
	cmdConnectTo := "curl -s -b cookies.txt --connect-to '" + originHost + ":localhost:" + portStr + "' 'http://" + originHost + "/ok'"

	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern:      "step 1 approve origin cookie",
			ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": cmdOrigin}}},
			FollowUpText: "Approved origin.",
		},
		{
			Pattern:      "step 2 quiet repeat origin cookie",
			ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": cmdOrigin}}},
			FollowUpText: "Repeated origin.",
		},
		{
			Pattern:      "step 3 redirect cookie to uncovered target",
			ToolCalls:    []llm.MockToolCall{{ID: "program-3", Name: "command", Args: map[string]any{"command": cmdRedirect}}},
			FollowUpText: "Redirect withheld secret cookie.",
		},
		{
			Pattern:      "step 4 connect-to uncovered recipient",
			ToolCalls:    []llm.MockToolCall{{ID: "program-4", Name: "command", Args: map[string]any{"command": cmdConnectTo}}},
			FollowUpText: "Connect-to withheld secret cookie.",
		},
		{
			Pattern:      "step 5a install target standing grant",
			ToolCalls:    []llm.MockToolCall{{ID: "program-5a", Name: "command", Args: map[string]any{"command": cmdTarget}}},
			FollowUpText: "Approved target.",
		},
		{
			Pattern:      "step 5b redirect after mid-flight target grant revocation",
			ToolCalls:    []llm.MockToolCall{{ID: "program-5b", Name: "command", Args: map[string]any{"command": cmdRedirect}}},
			FollowUpText: "Redirect re-asked after mid-flight target grant revocation.",
		},
		{
			Pattern:      "step 6a approve once after origin grant revocation",
			ToolCalls:    []llm.MockToolCall{{ID: "program-6a", Name: "command", Args: map[string]any{"command": cmdOrigin}}},
			FollowUpText: "Approved origin once.",
		},
		{
			Pattern:      "step 6b re-ask after once permit expires",
			ToolCalls:    []llm.MockToolCall{{ID: "program-6b", Name: "command", Args: map[string]any{"command": cmdOrigin}}},
			FollowUpText: "Withheld after once permit expired.",
		},
	}})

	approvals := "  - category: tool\n    pattern: http_request\n    effect: allow\n  - category: network_recipient\n    pattern: '*'\n    effect: allow\n  - category: file\n    pattern: '*'\n    effect: allow\n"
	s := startSessionWithClient(t, mock, wire.CreateSessionRequest{}, approvals, gate.PostureBalanced)
	s.h.SeedProgress(t, t.Context(), s.session)
	if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-cookie-real", "curl-fixture", nil) {
		t.Fatal("listener grant failed")
	}

	cookieJarText := strings.Join([]string{
		".cookie.example\tTRUE\t/\tFALSE\t0\tsession\t" + cookieSecret,
		".cookie.example\tTRUE\t/\tFALSE\t0\ttheme\tdark",
		"other.unselected.example\tFALSE\t/\tFALSE\t0\tunrelated\t" + unrelatedSecret,
	}, "\n") + "\n"
	testutil.FailErr(t, "write cookies.txt", os.WriteFile(filepath.Join(s.dir, "cookies.txt"), []byte(cookieJarText), 0o600))

	waitCheckpoint := func(promptText string) (wire.CheckpointEvent, <-chan error) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, promptText)
			done <- err
		}()
		kind := wire.CheckpointKindToolApproval
		var card wire.CheckpointEvent
		approved := map[string]bool{}
		testutil.WaitFor(t, func() bool {
			select {
			case err := <-done:
				testutil.FailErr(t, promptText, err)
				t.Fatalf("prompt %q ended without raising a tool_approval checkpoint", promptText)
			default:
			}
			pending, err := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
			if err != nil {
				return false
			}
			for _, candidate := range pending {
				// A first contact after the chat read the server's replies
				// asks on its own; this test is about the cookie disclosure.
				if candidate.ToolApproval.Plan.Subject.Kind != wire.ApprovalSubjectKindSecret {
					if !approved[candidate.ID] {
						approved[candidate.ID] = true
						answerCard(t, s, candidate, "approve_current_action")
					}
					continue
				}
				card = candidate
				return true
			}
			return false
		})
		return card, done
	}

	// A selected secret cookie asks once; the recommended option stores a standing grant.
	// RecommendedOptionID installs a persistent secret grant in s.h.Settings.Approvals.
	card1, done1 := waitCheckpoint("step 1 approve origin cookie")
	if originHits.Load() != 0 {
		t.Fatalf("step 1 hit origin before checkpoint resolution: %d", originHits.Load())
	}
	encoded1, err := json.Marshal(card1)
	testutil.FailErr(t, "marshal card1", err)
	if !strings.Contains(string(encoded1), "origin.cookie.example") || strings.Contains(string(encoded1), cookieSecret) {
		t.Fatalf("step 1 card must name origin.cookie.example without leaking secret value: %s", encoded1)
	}
	owner := s.h.OwnerCtx(t, t.Context())
	resp1, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(owner, s.session, card1.ID, card1.ToolApproval.Plan.RecommendedOptionID)
	testutil.FailErr(t, "resolve card1 with RecommendedOptionID", err)
	if resp1 == nil || resp1.Result == nil || len(resp1.Result.GrantIDs) == 0 {
		t.Fatalf("step 1 expected installed standing grant IDs, got %+v", resp1)
	}
	originGrantIDs := append([]string(nil), resp1.Result.GrantIDs...)
	testutil.FailErr(t, "step 1 prompt", <-done1)
	out1 := s.outcome(t, "program-1")
	if out1.State != "completed" || out1.Status != 0 || strings.TrimSpace(out1.Stdout) != "origin-cookie-ok" {
		t.Fatalf("step 1 outcome = %+v", out1)
	}
	if originHits.Load() != 1 || !strings.Contains(lastOriginCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 1 originHits=%d cookie=%q, want 1 with session secret", originHits.Load(), lastOriginCookie.Load())
	}

	// 2. A repeat to the same covered origin + dial override needs no new
	// cookie decision. Step 1 reached the origin while the chat was clean and
	// then read its reply, so the agent's next send there is a first contact
	// and asks once on its own.
	originHits.Store(0)
	lastOriginCookie.Store("")
	done2 := make(chan error, 1)
	go func() {
		_, err := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, "step 2 quiet repeat origin cookie")
		done2 <- err
	}()
	kind := wire.CheckpointKindToolApproval
	answered := map[string]bool{}
	testutil.WaitFor(t, func() bool {
		select {
		case err := <-done2:
			testutil.FailErr(t, "step 2 quiet repeat", err)
			return true
		default:
		}
		pending, _ := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
		for _, candidate := range pending {
			if candidate.ToolApproval.Plan.Subject.Kind == wire.ApprovalSubjectKindSecret {
				t.Fatalf("step 2 asked about the covered cookie again: %+v", candidate.ToolApproval.Plan)
			}
			if !answered[candidate.ID] {
				answered[candidate.ID] = true
				answerCard(t, s, candidate, "approve_current_action")
			}
		}
		return false
	})
	out2 := s.outcome(t, "program-2")
	if out2.State != "completed" || out2.Status != 0 || strings.TrimSpace(out2.Stdout) != "origin-cookie-ok" || originHits.Load() != 1 || !strings.Contains(lastOriginCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 2 quiet repeat outcome = %+v originHits=%d cookie=%q", out2, originHits.Load(), lastOriginCookie.Load())
	}

	// A redirect to an uncovered target asks before the second hop.
	// cookie and then raises a secret approval card for target.cookie.example before Hop 1; rejecting withholds
	// only the secret cookie on Hop 1 while preserving benign cookies and completing the redirect.
	originHits.Store(0)
	targetHits.Store(0)
	lastOriginCookie.Store("")
	lastTargetCookie.Store("")
	card3, done3 := waitCheckpoint("step 3 redirect cookie to uncovered target")
	if originHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("step 3 hits while waiting on redirect secret card: origin=%d target=%d, want 1/0", originHits.Load(), targetHits.Load())
	}
	if !strings.Contains(lastOriginCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 3 hop 0 cookie = %q, want session secret", lastOriginCookie.Load())
	}
	encoded3, err := json.Marshal(card3)
	testutil.FailErr(t, "marshal card3", err)
	if !strings.Contains(string(encoded3), "target.cookie.example") {
		t.Fatalf("step 3 redirect card must name target.cookie.example: %s", encoded3)
	}
	_, err = s.h.Sessions.Checkpoints.ResolveCheckpoint(owner, s.session, card3.ID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "reject card3", err)
	testutil.FailErr(t, "step 3 prompt", <-done3)
	out3 := s.outcome(t, "program-3")
	if out3.State != "completed" || out3.Status != 0 || strings.TrimSpace(out3.Stdout) != "target-cookie-ok" {
		t.Fatalf("step 3 redirect secret withhold outcome = %+v / refusal=%+v", out3, out3.Refusal)
	}
	if originHits.Load() != 1 || targetHits.Load() != 1 || lastTargetCookie.Load().(string) != "theme=dark" {
		t.Fatalf("step 3 final hits/cookies: origin=%d target=%d targetCookie=%q, want 1/1 and \"theme=dark\" without secret", originHits.Load(), targetHits.Load(), lastTargetCookie.Load())
	}

	// A changed --connect-to route asks before dialing; rejecting withholds only the cookie.
	// existing grant and asks before Hop 0 dials; rejecting withholds only the secret cookie on Hop 0 while
	// preserving benign cookies.
	originHits.Store(0)
	lastOriginCookie.Store("")
	card4, done4 := waitCheckpoint("step 4 connect-to uncovered recipient")
	if originHits.Load() != 0 {
		t.Fatalf("step 4 hit origin before connect-to secret card resolution: %d", originHits.Load())
	}
	_, err = s.h.Sessions.Checkpoints.ResolveCheckpoint(owner, s.session, card4.ID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "reject card4", err)
	testutil.FailErr(t, "step 4 prompt", <-done4)
	out4 := s.outcome(t, "program-4")
	if out4.State != "completed" || out4.Status != 0 || strings.TrimSpace(out4.Stdout) != "origin-cookie-ok" || originHits.Load() != 1 || lastOriginCookie.Load().(string) != "theme=dark" {
		t.Fatalf("step 4 connect-to secret withhold outcome = %+v / refusal=%+v originHits=%d originCookie=%q", out4, out4.Refusal, originHits.Load(), lastOriginCookie.Load())
	}

	// Revoking the target's grant during the first hop makes the second hop ask again.
	// target.cookie.example, then revoke it while Hop 0 (/redirect) is served; Hop 1 must query the live
	// approval store, raise a checkpoint before targetHits increments, and withhold the secret cookie on
	// rejection while completing the redirect.
	targetHits.Store(0)
	lastTargetCookie.Store("")
	card5a, done5a := waitCheckpoint("step 5a install target standing grant")
	resp5a, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(owner, s.session, card5a.ID, card5a.ToolApproval.Plan.RecommendedOptionID)
	testutil.FailErr(t, "resolve card5a with RecommendedOptionID", err)
	if resp5a == nil || resp5a.Result == nil || len(resp5a.Result.GrantIDs) == 0 {
		t.Fatalf("step 5a expected target standing grant IDs, got %+v", resp5a)
	}
	targetGrantIDs := append([]string(nil), resp5a.Result.GrantIDs...)
	testutil.FailErr(t, "step 5a prompt", <-done5a)
	if targetHits.Load() != 1 || !strings.Contains(lastTargetCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 5a targetHits=%d cookie=%q, want 1 with session secret", targetHits.Load(), lastTargetCookie.Load())
	}

	onRedirectRevoke = func() {
		for _, id := range targetGrantIDs {
			removed, revokeErr := s.h.Server.Admin.Capabilities.Access.Gate.RevokeGrant(id)
			testutil.FailErr(t, "revoke target standing grant mid-redirect", revokeErr)
			if !removed {
				t.Errorf("expected target standing grant %q to be removed mid-redirect", id)
			}
		}
	}
	revokeTargetOnRedirect.Store(true)
	originHits.Store(0)
	targetHits.Store(0)
	lastOriginCookie.Store("")
	lastTargetCookie.Store("")
	card5b, done5b := waitCheckpoint("step 5b redirect after mid-flight target grant revocation")
	if originHits.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("step 5b hits while waiting on redirect secret card after mid-flight revocation: origin=%d target=%d, want 1/0", originHits.Load(), targetHits.Load())
	}
	if !strings.Contains(lastOriginCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 5b hop 0 cookie = %q, want session secret", lastOriginCookie.Load())
	}
	_, err = s.h.Sessions.Checkpoints.ResolveCheckpoint(owner, s.session, card5b.ID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "reject card5b", err)
	testutil.FailErr(t, "step 5b prompt", <-done5b)
	out5b := s.outcome(t, "program-5b")
	if out5b.State != "completed" || out5b.Status != 0 || strings.TrimSpace(out5b.Stdout) != "target-cookie-ok" || originHits.Load() != 1 || targetHits.Load() != 1 || lastTargetCookie.Load().(string) != "theme=dark" {
		t.Fatalf("step 5b outcome = %+v origin=%d target=%d targetCookie=%q, want completed 1/1 and \"theme=dark\"", out5b, originHits.Load(), targetHits.Load(), lastTargetCookie.Load())
	}

	// A once approval covers one operation and does not persist.
	// covers only that single admitted operation and does not persist to the next operation.
	for _, id := range originGrantIDs {
		removed, revokeErr := s.h.Server.Admin.Capabilities.Access.Gate.RevokeGrant(id)
		testutil.FailErr(t, "revoke origin standing grant", revokeErr)
		if !removed {
			t.Fatalf("expected origin standing grant %q to be removed", id)
		}
	}
	originHits.Store(0)
	lastOriginCookie.Store("")
	card6a, done6a := waitCheckpoint("step 6a approve once after origin grant revocation")
	if originHits.Load() != 0 {
		t.Fatalf("step 6a hit origin before checkpoint resolution: %d", originHits.Load())
	}
	resp6a, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(owner, s.session, card6a.ID, "send_unchanged")
	testutil.FailErr(t, "approve card6a once", err)
	if resp6a == nil || resp6a.Result == nil || len(resp6a.Result.GrantIDs) != 0 {
		t.Fatalf("step 6a send_unchanged must not install a standing grant, got %+v", resp6a)
	}
	testutil.FailErr(t, "step 6a prompt", <-done6a)
	out6a := s.outcome(t, "program-6a")
	if out6a.State != "completed" || out6a.Status != 0 || strings.TrimSpace(out6a.Stdout) != "origin-cookie-ok" || originHits.Load() != 1 || !strings.Contains(lastOriginCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 6a once-approved outcome = %+v originHits=%d cookie=%q", out6a, originHits.Load(), lastOriginCookie.Load())
	}

	originHits.Store(0)
	lastOriginCookie.Store("")
	card6b, done6b := waitCheckpoint("step 6b re-ask after once permit expires")
	if originHits.Load() != 0 {
		t.Fatalf("step 6b hit origin before checkpoint resolution: %d", originHits.Load())
	}
	_, err = s.h.Sessions.Checkpoints.ResolveCheckpoint(owner, s.session, card6b.ID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "reject card6b", err)
	testutil.FailErr(t, "step 6b prompt", <-done6b)
	out6b := s.outcome(t, "program-6b")
	if out6b.State != "completed" || out6b.Status != 0 || strings.TrimSpace(out6b.Stdout) != "origin-cookie-ok" || originHits.Load() != 1 || lastOriginCookie.Load().(string) != "theme=dark" {
		t.Fatalf("step 6b withhold after once expiry outcome = %+v originHits=%d cookie=%q, want 1 and \"theme=dark\"", out6b, originHits.Load(), lastOriginCookie.Load())
	}
}

func TestCommandProgramCurlSecretDeliveryTimingAndUnlockLifecycle(t *testing.T) {
	const (
		ignoredHeaderSecret = "AKIAQYJK5TXV4NZR7SGB"
		cookieSecret        = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	)

	var hits atomic.Int32
	var seenAuth, seenCookie atomic.Value
	seenAuth.Store("")
	seenCookie.Store("")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		seenAuth.Store(r.Header.Get("X-Ignored-Key"))
		seenCookie.Store(r.Header.Get("Cookie"))
		_, _ = io.WriteString(w, "timing-e2e-ok\n")
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	testutil.FailErr(t, "parse proxy listener", err)
	portStr := proxyURL.Port()
	port, err := strconv.ParseUint(portStr, 10, 16)
	testutil.FailErr(t, "proxy port", err)
	host := "timing.secret.example:" + portStr
	resolveHost := host + ":127.0.0.1"

	cmdCookieAndHeader := "curl -s -b cookies.txt -H 'X-Ignored-Key: " + ignoredHeaderSecret + "' --resolve '" + resolveHost + "' 'http://" + host + "/deploy'"

	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern:      "step 1 withdraw classification while paused on cookie checkpoint",
			ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": cmdCookieAndHeader}}},
			FollowUpText: "Classification withdrawn during checkpoint.",
		},
		{
			Pattern:      "step 2 cookie withhold continues with active classification",
			ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": cmdCookieAndHeader}}},
			FollowUpText: "Completed without denied cookie.",
		},
	}})
	approvals := "  - category: tool\n    pattern: http_request\n    effect: allow\n  - category: network_recipient\n    pattern: '*'\n    effect: allow\n  - category: file\n    pattern: '*'\n    effect: allow\n"
	s := startSessionWithClient(t, mock, wire.CreateSessionRequest{}, approvals, gate.PostureBalanced)
	s.h.SeedProgress(t, t.Context(), s.session)
	if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "curl-timing-e2e", "curl-fixture", nil) {
		t.Fatal("listener grant failed")
	}

	var ignoreActive atomic.Bool
	ignoreActive.Store(true)
	s.h.Server.Admin.Project.Secrets.SecretIgnores.Trusted = func(context.Context, string) bool {
		return ignoreActive.Load()
	}
	s.h.Server.Admin.Project.Secrets.SecretIgnores.Roots = func(context.Context, string) ([]projectignore.Root, error) {
		return []projectignore.Root{{ID: "r1", Path: s.dir}}, nil
	}
	testutil.FailErr(t, "add secret ignore", s.h.Server.Admin.Project.Secrets.SecretIgnores.Add(t.Context(), s.project, "r1", projectignore.SecretEntry{
		ID:     "fa40d86b-836e-4006-b74b-63c88c889eef",
		Value:  ignoredHeaderSecret,
		Reason: "public test key",
	}))

	cookieJarText := strings.Join([]string{
		".secret.example\tTRUE\t/\tFALSE\t0\tsession\t" + cookieSecret,
		".secret.example\tTRUE\t/\tFALSE\t0\ttheme\tdark",
	}, "\n") + "\n"
	testutil.FailErr(t, "write cookies.txt", os.WriteFile(filepath.Join(s.dir, "cookies.txt"), []byte(cookieJarText), 0o600))

	waitCookieCard := func(promptText string) (wire.CheckpointEvent, <-chan error) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, promptErr := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, promptText)
			done <- promptErr
		}()
		kind := wire.CheckpointKindToolApproval
		var card wire.CheckpointEvent
		testutil.WaitFor(t, func() bool {
			select {
			case promptErr := <-done:
				testutil.FailErr(t, promptText, promptErr)
				t.Fatalf("prompt %q ended without raising a tool_approval checkpoint", promptText)
			default:
			}
			pending, listErr := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
			if listErr != nil || len(pending) == 0 {
				return false
			}
			card = pending[0]
			return true
		})
		return card, done
	}

	owner := s.h.OwnerCtx(t, t.Context())

	// Withdrawing the classification during the first hop's cookie review
	// refuses before any dial.
	cookieCard1, done1 := waitCookieCard("step 1 withdraw classification while paused on cookie checkpoint")
	if hits.Load() != 0 {
		t.Fatalf("step 1 hit server before cookie checkpoint resolution: %d", hits.Load())
	}
	ignoreActive.Store(false)
	_, err = s.h.Sessions.Checkpoints.ResolveCheckpoint(owner, s.session, cookieCard1.ID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "withhold cookie on card1", err)
	testutil.FailErr(t, "step 1 prompt", <-done1)
	out1 := s.outcome(t, "program-1")
	if out1.State != "rejected" || out1.Refusal == nil || out1.Refusal.Code != "OUTBOUND_SECRET_SCREEN_FAILED" {
		t.Fatalf("step 1 classification-withdrawn-during-wait outcome = %+v / refusal=%+v, want rejected OUTBOUND_SECRET_SCREEN_FAILED", out1, out1.Refusal)
	}
	if hits.Load() != 0 {
		t.Fatalf("step 1 server hits = %d, want 0", hits.Load())
	}

	// Withholding the secret cookie sends the request without it.
	ignoreActive.Store(true)
	hits.Store(0)
	seenAuth.Store("")
	seenCookie.Store("")
	cookieCard2, done2 := waitCookieCard("step 2 cookie withhold continues with active classification")
	_, err = s.h.Sessions.Checkpoints.ResolveCheckpoint(owner, s.session, cookieCard2.ID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "withhold cookie on step 2", err)
	testutil.FailErr(t, "step 2 prompt", <-done2)
	out2 := s.outcome(t, "program-2")
	if out2.State != "completed" || out2.Status != 0 || strings.TrimSpace(out2.Stdout) != "timing-e2e-ok" {
		t.Fatalf("step 2 outcome = %+v / refusal=%+v, want completed timing-e2e-ok", out2, out2.Refusal)
	}
	if hits.Load() != 1 || seenAuth.Load().(string) != ignoredHeaderSecret || seenCookie.Load().(string) != "theme=dark" {
		t.Fatalf("step 2 hits=%d header=%q cookie=%q, want 1, %s, theme=dark", hits.Load(), seenAuth.Load(), seenCookie.Load(), ignoredHeaderSecret)
	}
}
