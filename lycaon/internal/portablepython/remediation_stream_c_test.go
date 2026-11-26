package portablepython_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// stubLiveEditorDocuments implements tools.EditorDocuments for live unsaved
// draft capture through toolhost's registered `python` handler (Finding 9).
type stubLiveEditorDocuments struct {
	tools.EditorDocuments
	docs map[string]tools.EditorDocumentText
}

func (s *stubLiveEditorDocuments) OpenDocument(
	_ context.Context,
	_ string,
	_ sourcebranch.ID,
	rootID, path string,
) (tools.EditorDocumentText, bool, error) {
	if doc, ok := s.docs[rootID+":"+path]; ok {
		return doc, true, nil
	}
	if doc, ok := s.docs[path]; ok {
		return doc, true, nil
	}
	return tools.EditorDocumentText{}, false, nil
}

func (s *stubLiveEditorDocuments) FreezeAgentReads(_, _ string) map[string]documentcore.ReadBasis {
	return nil
}

func TestStreamCRemediationCommandEffectsFailClosedProtectedPathsEditorAndEvidence(t *testing.T) {
	svc := newRealPortablePythonService(t)
	stockCatalog := extpackstest.StockCatalog(t)

	// Finding 2: Profile/surface command_effects enforced through toolhost.
	t.Run("read_only surface stops python write with COMMAND_EFFECT_NOT_ALLOWED", func(t *testing.T) {
		rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{
			Catalog:        stockCatalog,
			PortablePython: svc,
		})
		testutil.FailErr(t, "construct toolhost runtime", err)

		wsDir := t.TempDir()
		tc := tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID:  "sess-ro-surface",
				Agent:      "agent:planner",
				ToolCallID: "call-ro-surface",
			},
			Turn: tools.InvocationTurn{
				TurnSurfaceID: "orchestrate_plan",
			},
			Source: tools.InvocationSource{
				ActiveRootID: "root-main",
				Roots: []projectroot.RootRef{{
					ID:    "root-main",
					Label: "main",
					Path:  wsDir,
				}},
				// Deliberately false so enforcement MUST come from command_effects!
				ReadOnly: false,
				AskMode:  false,
			},
		}

		raw, err := rt.Registry.Run(context.Background(), "python", map[string]any{
			"code": `open("blocked.txt", "w").write("should not exist")`,
		}, tc)
		testutil.FailErr(t, "run python on read-only surface", err)

		var resp portablepython.ToolResponse
		testutil.FailErr(t, "decode response", json.Unmarshal([]byte(raw), &resp))
		if resp.CleanCompletion {
			t.Fatalf("expected non-clean stop on read-only surface, got clean response: %+v", resp)
		}
		if resp.Stop == nil || resp.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("stop = %+v, want COMMAND_EFFECT_NOT_ALLOWED", resp.Stop)
		}
		if _, statErr := os.Stat(filepath.Join(wsDir, "blocked.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("blocked.txt must not exist on disk after read-only surface refusal, statErr=%v", statErr)
		}
	})

	// Finding 2 & 3: AskMode / no network_egress AND nil policy both fail closed
	// for outbound HTTP with zero connections.
	t.Run("outbound HTTP fails closed without policy or without network_egress", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			_, _ = w.Write([]byte("should-never-connect"))
		}))
		defer srv.Close()

		// (a) Direct Service.ExecuteTool with Policy == nil fails closed.
		respNilPol, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:       "exec-http-nil-policy",
			CallID:            "call-http-nil-policy",
			SessionID:         "sess-http-fc",
			Actor:             "agent:coder",
			View:              "workspace",
			Mode:              pythonruntime.ModeCode,
			Code:              `import requests, sys; requests.get(sys.argv[1])`,
			Args:              []string{srv.URL + "/probe"},
			AllowLoopbackHTTP: true,
			Policy:            nil,
		})
		testutil.FailErr(t, "execute http with nil policy", err)
		if respNilPol.CleanCompletion || respNilPol.Stop == nil || respNilPol.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("nil policy http = %+v, want COMMAND_EFFECT_NOT_ALLOWED", respNilPol)
		}

		// (b) Registered toolhost python handler on a surface without
		// network_egress (orchestrate_plan) stops HTTPS with COMMAND_EFFECT_NOT_ALLOWED and 0 hits.
		var tlsHits atomic.Int32
		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			tlsHits.Add(1)
			_, _ = w.Write([]byte("should-never-connect"))
		}))
		defer tlsSrv.Close()

		rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{
			Catalog:        stockCatalog,
			PortablePython: svc,
		})
		testutil.FailErr(t, "construct toolhost runtime", err)
		raw, err := rt.Registry.Run(context.Background(), "python", map[string]any{
			"code": `import requests, sys; requests.get(sys.argv[1])`,
			"args": []any{tlsSrv.URL + "/no-egress-surface"},
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID:  "sess-no-egress",
				Agent:      "agent:planner",
				ToolCallID: "call-no-egress",
			},
			Turn: tools.InvocationTurn{
				TurnSurfaceID: "orchestrate_plan",
			},
		})
		testutil.FailErr(t, "run python http on surface without network_egress", err)
		var respNoEgress portablepython.ToolResponse
		testutil.FailErr(t, "decode no-egress response", json.Unmarshal([]byte(raw), &respNoEgress))
		if respNoEgress.CleanCompletion || respNoEgress.Stop == nil || respNoEgress.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("no-egress surface http = %+v, want COMMAND_EFFECT_NOT_ALLOWED", respNoEgress)
		}
		if hits.Load() != 0 || tlsHits.Load() != 0 {
			t.Fatalf("server received %d http / %d https hits under fail-closed HTTP policy, want 0", hits.Load(), tlsHits.Load())
		}
	})

	// Finding 10: DefaultProtectedPaths blocks writes to .git/hooks/pre-commit and .git/config.
	t.Run("default protected paths block .git/hooks and .git/config", func(t *testing.T) {
		wsDir := t.TempDir()
		testutil.FailErr(t, "mkdir .git/hooks", os.MkdirAll(filepath.Join(wsDir, ".git", "hooks"), 0o755))
		testutil.FailErr(t, "write .git/config", os.WriteFile(filepath.Join(wsDir, ".git", "config"), []byte("[core]\n"), 0o644))

		roots := []pythondocuments.HostRootConfig{{
			ID:  "root-git",
			Dir: wsDir,
		}}
		tc := tools.ToolContext{
			Source: tools.InvocationSource{
				ActiveRootID: "root-git",
				Roots:        []projectroot.RootRef{{ID: "root-git", Label: "main", Path: wsDir}},
			},
		}
		door := native.NewPythonMutationDoor(tc, roots)

		for _, target := range []string{".git/hooks/pre-commit", ".git/config"} {
			resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
				ExecutionID:  "exec-prot-" + filepath.Base(target),
				SessionID:    "sess-prot",
				Actor:        "agent:coder",
				View:         "workspace",
				Mode:         pythonruntime.ModeCode,
				Code:         `import sys; open(sys.argv[1], "w").write("pwned")`,
				Args:         []string{target},
				Roots:        roots,
				MutationDoor: door,
			})
			testutil.FailErr(t, "execute protected write "+target, err)
			if resp.CleanCompletion || resp.Stop == nil || resp.Stop.Code != pythonruntime.StopEffectNotAllowed {
				t.Fatalf("protected path %s = %+v, want COMMAND_EFFECT_NOT_ALLOWED", target, resp)
			}
		}
		if _, err := os.Stat(filepath.Join(wsDir, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
			t.Fatalf(".git/hooks/pre-commit must not exist, err=%v", err)
		}
		gotCfg, err := os.ReadFile(filepath.Join(wsDir, ".git", "config"))
		testutil.FailErr(t, "read .git/config", err)
		if string(gotCfg) != "[core]\n" {
			t.Fatalf(".git/config was modified: %q", string(gotCfg))
		}
	})

	// Finding 9: Live tc.Source.EditorDocuments unsaved draft + multi-root @label mounting.
	t.Run("live EditorDocuments unsaved draft and multi-root @label through toolhost", func(t *testing.T) {
		rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{
			Catalog:        stockCatalog,
			PortablePython: svc,
		})
		testutil.FailErr(t, "construct toolhost runtime", err)

		mainDir := t.TempDir()
		docsDir := t.TempDir()
		testutil.FailErr(t, "write disk notes.txt", os.WriteFile(filepath.Join(mainDir, "notes.txt"), []byte("DISK_SAVED_VERSION\n"), 0o644))
		testutil.FailErr(t, "write docs guide.txt", os.WriteFile(filepath.Join(docsDir, "guide.txt"), []byte("SECOND_ROOT_GUIDE\n"), 0o644))

		liveEditor := &stubLiveEditorDocuments{
			docs: map[string]tools.EditorDocumentText{
				"root-main:notes.txt": {
					ID:       "doc-live-1",
					RootID:   "root-main",
					Path:     "notes.txt",
					Revision: 7,
					Text:     "UNSAVED_LIVE_EDITOR_DRAFT\n",
					Dirty:    true,
				},
			},
		}

		tc := tools.ToolContext{
			Identity: tools.InvocationIdentity{
				ProjectID:  "proj-live-editor",
				SessionID:  "sess-live-editor",
				Agent:      "agent:coder",
				ToolCallID: "call-live-editor",
			},
			Source: tools.InvocationSource{
				SourceWorkspaceKind: api.SourceWorkspaceKindProject,
				ActiveRootID:        "root-main",
				EditorDocuments:     liveEditor,
				Roots: []projectroot.RootRef{
					{ID: "root-main", Label: "main", Path: mainDir},
					{ID: "root-docs", Label: "docs", Path: docsDir},
				},
			},
		}

		raw, err := rt.Registry.Run(context.Background(), "python", map[string]any{
			"code": `print(open("notes.txt").read().strip() + "|" + open("@docs/guide.txt").read().strip())`,
		}, tc)
		testutil.FailErr(t, "run python reading live draft and @docs root", err)

		var resp portablepython.ToolResponse
		testutil.FailErr(t, "decode live editor response", json.Unmarshal([]byte(raw), &resp))
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("live editor run failed: %+v", resp)
		}
		if strings.TrimSpace(resp.Stdout) != "UNSAVED_LIVE_EDITOR_DRAFT|SECOND_ROOT_GUIDE" {
			t.Fatalf("stdout = %q, want %q", strings.TrimSpace(resp.Stdout), "UNSAVED_LIVE_EDITOR_DRAFT|SECOND_ROOT_GUIDE")
		}
	})

	// Finding 6 & 8: Secret screening, bounded inline output + RetainedArtifactStore overflow,
	// and non-nil EvidenceSummary on ToolResponse.
	t.Run("secret screening bounded inline output retained overflow and evidence summary", func(t *testing.T) {
		// (a) Secret screening redacts API keys & GitHub tokens before returning.
		secretResp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID: "exec-secret-screen",
			CallID:      "call-secret-screen",
			SessionID:   "sess-evidence",
			Actor:       "agent:coder",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `print("key=sk-live-secret1234567890abcdef tok=ghp_1234567890abcdefghijklmnop1234567890")`,
		})
		testutil.FailErr(t, "execute secret screening run", err)
		if !secretResp.CleanCompletion || secretResp.ExitCode != 0 {
			t.Fatalf("secret screening run failed: %+v", secretResp)
		}
		if strings.Contains(secretResp.Stdout, "sk-live-secret1234567890abcdef") || strings.Contains(secretResp.Stdout, "ghp_1234567890") {
			t.Fatalf("secret leaked in ToolResponse.Stdout: %q", secretResp.Stdout)
		}
		if secretResp.RedactedCount <= 0 {
			t.Fatalf("expected RedactedCount > 0, got %d", secretResp.RedactedCount)
		}
		if secretResp.EvidenceSummary == nil || !secretResp.EvidenceSummary.Visible {
			t.Fatalf("expected non-nil visible EvidenceSummary, got %+v", secretResp.EvidenceSummary)
		}

		// (b) Output exceeding 65536-byte inline budget is truncated inline and
		// retained in RetainedArtifactStore with a retrievable artifact ID.
		bigResp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID: "exec-big-output",
			CallID:      "call-big-output",
			SessionID:   "sess-evidence",
			Actor:       "agent:coder",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `print("A" * 100000)`,
		})
		testutil.FailErr(t, "execute big output run", err)
		if !bigResp.CleanCompletion || bigResp.ExitCode != 0 {
			t.Fatalf("big output run failed: %+v", bigResp)
		}
		if bigResp.InlineBudgetBytes != portablepython.DefaultInlineBudgetBytes {
			t.Fatalf("InlineBudgetBytes = %d, want %d", bigResp.InlineBudgetBytes, portablepython.DefaultInlineBudgetBytes)
		}
		if !bigResp.StdoutTruncated || len(bigResp.Stdout) != portablepython.DefaultInlineBudgetBytes {
			t.Fatalf("expected truncated inline stdout of %d bytes, got truncated=%v len=%d",
				portablepython.DefaultInlineBudgetBytes, bigResp.StdoutTruncated, len(bigResp.Stdout))
		}
		if bigResp.RetainedStdoutArtifactID == "" {
			t.Fatal("expected non-empty RetainedStdoutArtifactID for overflow stdout")
		}
		art, err := svc.RetainedStore().Get(context.Background(), pythonevidence.ViewerScope{
			Role:      pythonevidence.RoleCoordinator,
			SessionID: "sess-evidence",
			Actor:     "agent:coder",
			View:      "workspace",
		}, bigResp.RetainedStdoutArtifactID)
		testutil.FailErr(t, "fetch retained stdout artifact", err)
		if len(art.Data) < 100000 {
			t.Fatalf("retained stdout artifact length = %d, want >= 100000", len(art.Data))
		}
	})
}
