package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestWebResearchPromptVarsSuppliedAndGated ensures templates that branch on
// web_search_enabled / can_spawn_web_research receive those vars from MergeForTurn,
// and that false renders omit web tool names.
func TestWebResearchPromptVarsSuppliedAndGated(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	catalogRoot := filepath.Join(lycaonRoot, "config", "packs")

	var gated []string
	_ = filepath.Walk(catalogRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		if strings.Contains(text, "web_search_enabled") || strings.Contains(text, "can_spawn_web_research") {
			gated = append(gated, path)
		}
		return nil
	})
	if len(gated) == 0 {
		t.Fatal("expected catalog templates referencing web research prompt vars")
	}

	// Assembly must wire MergeForTurn so render sites cannot silently omit the vars.
	asmPath := filepath.Join(lycaonRoot, "internal", "coordinator", "assembly", "tripartite_prompt.go")
	asm, err := os.ReadFile(asmPath)
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(asm), "MergeForTurn(") {
		t.Fatalf("%s must call capability.MergeForTurn", asmPath)
	}
	capPath := filepath.Join(lycaonRoot, "internal", "coordinator", "capability", "capability.go")
	capBody, err := os.ReadFile(capPath)
	contractcheck.FailErr(t, "read file", err)
	capSrc := string(capBody)
	if !strings.Contains(capSrc, `into["web_search_enabled"]`) {
		t.Fatalf("%s must set web_search_enabled", capPath)
	}
	if !strings.Contains(capSrc, `into["can_spawn_web_research"]`) && !strings.Contains(capSrc, "can_spawn_web_research") {
		t.Fatalf("%s must set can_spawn_web_research via MergeVars", capPath)
	}

	engine := contractcheck.BundledPromptEngineForRoot(t)
	for _, enabled := range []bool{false, true} {
		vars := map[string]any{
			"execution_mode": "orchestrate",
			"root_count":     0,
		}
		contractcheck.FailErr(t, "MergeForTurn", capability.MergeForTurn(
			vars,
			surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
			0,
			inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 0, false, enabled).Effective,
			enabled,
		))
		if vars["web_search_enabled"] != enabled {
			t.Fatalf("web_search_enabled = %v want %v", vars["web_search_enabled"], enabled)
		}
		_, ok := vars["can_spawn_web_research"].(bool)
		if !ok {
			t.Fatal("can_spawn_web_research missing after MergeForTurn")
		}
		rendered, err := engine.Render(context.Background(), "agents/coordinator-core.md", vars)
		contractcheck.FailErr(t, "render coordinator-core", err)
		if !enabled {
			for _, forbid := range []string{"`web_search`", "`fetch_url`", "web-researcher"} {
				if strings.Contains(rendered, forbid) {
					t.Fatalf("enabled=false must not mention %s in coordinator-core", forbid)
				}
			}
		}

		// Folder sessions must keep the freshness clause on Invariant 5 (not only the
		// no-folder External-claims branch / soft Stale-training partial).
		folderVars := map[string]any{
			"execution_mode":     "investigate",
			"has_file_tools":     true,
			"web_search_enabled": enabled,
		}
		folderRendered, err := engine.Render(context.Background(), "agents/coordinator-core.md", folderVars)
		contractcheck.FailErr(t, "render coordinator-core folder", err)
		hasFresh := strings.Contains(folderRendered, "Stale-training check:") &&
			strings.Contains(folderRendered, "require this-turn `web_search` + `fetch_url` evidence")
		if enabled {
			if !hasFresh {
				t.Fatalf("enabled=true + has_file_tools must keep the stale-training check")
			}
		} else if hasFresh {
			t.Fatalf("enabled=false + has_file_tools must omit the stale-training check")
		}
		// The no-web branch still rejects unsupported recall.
		if !enabled && !strings.Contains(folderRendered, "**External facts:**") {
			t.Fatal("enabled=false must still state that training recall is not evidence")
		}
	}

	_ = gated // The core render covers gated partials.
}
