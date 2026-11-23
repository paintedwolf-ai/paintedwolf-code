package prompts_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func TestFileTemplateEngine_RenderGuidance(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.RenderGuidance(context.Background(), "reject/_reject", map[string]any{
		"code": "TEST", "what": "blocked", "cause": "blocked", "why": "blocked", "fix": "retry", "instead": "retry", "category": "recoverable",
	})
	testutil.FailErr(t, "engine.RenderGuidance failed", err)
	if !strings.Contains(out, "TEST") || !strings.Contains(out, "Rejected:") {
		t.Fatalf("out = %q", out)
	}
}

func TestFileTemplateEngine_RenderKick(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	reader, err := prompts.DeclaredReader(nil, prompts.CoordinatorProfileID)
	testutil.FailErr(t, "DeclaredReader coordinator", err)
	out, err := engine.RenderKick(context.Background(), reader, "coordinator-gate-blocked", nil)
	testutil.FailErr(t, "engine.RenderKick failed", err)
	if !strings.Contains(out, "failed_leaves") {
		t.Fatalf("out = %q", out)
	}
}

func TestFileTemplateEngine_RenderInjectScanEphemeral(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	_, err := engine.RenderInject(context.Background(), "scan-guidance-ephemeral", map[string]any{
		"summaries": []map[string]any{{"code": "X", "message": "msg"}},
	})
	testutil.FailErr(t, "engine.RenderInject failed", err)
}

func TestRenderGuidance_RejectsPathEscape(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	_, err := engine.RenderGuidance(context.Background(), "../agents/coordinator", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid template ref") {
		t.Fatalf("err = %v", err)
	}
}

func TestModelFacingRenderEntryPointsRequireReader(t *testing.T) {
	ctx := context.Background()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	gr := prompts.NewGuidanceRenderer(engine)
	ir := prompts.NewInjectRenderer(engine)
	ur := prompts.NewUtilityPromptRenderer(engine)

	t.Run("zero reader fails closed on model-facing entry points", func(t *testing.T) {
		if _, err := prompts.Vars(prompts.Reader{}, nil); err == nil {
			t.Fatal("prompts.Vars accepted zero Reader")
		}
		if _, err := engine.RenderKick(ctx, prompts.Reader{}, "coordinator-gate-blocked", nil); err == nil {
			t.Fatal("engine.RenderKick accepted zero Reader")
		}
		if _, err := gr.RenderWithReader(ctx, prompts.Reader{}, "board-orientation", nil); err == nil {
			t.Fatal("GuidanceRenderer.RenderWithReader accepted zero Reader")
		}
		if _, err := ir.RenderWithReader(ctx, prompts.Reader{}, "turn-state", nil); err == nil {
			t.Fatal("InjectRenderer.RenderWithReader accepted zero Reader")
		}
		if _, err := prompts.AuthorRender(ctx, prompts.AuthorRenderRequest{
			TemplateRef: "guidance/board-orientation.md",
		}); err == nil {
			t.Fatal("AuthorRender accepted zero PreviewTarget")
		}
	})

	t.Run("reader-free renderers reject reader-conditioned and utility templates", func(t *testing.T) {
		for _, ref := range prompts.ZeroToolUtilityTemplates() {
			if _, err := gr.Render(ctx, ref, nil); err == nil {
				t.Fatalf("GuidanceRenderer.Render accepted zero-tool utility ref %q", ref)
			}
		}
		for _, ref := range []string{
			"board-orientation", "command-jobs", "source-changes",
			"coordinator-iterations-low", "coordinator-spend-runway-low",
			"utility/command-output-omitted", "compaction/continuation-record",
			"recall/body-missing", "enrich/overlay-rebase-conflict",
			"survey/grep-specifics",
		} {
			if _, err := gr.Render(ctx, ref, nil); err == nil {
				t.Fatalf("GuidanceRenderer.Render accepted non-structural ref %q without Reader or toolreach.Request", ref)
			}
		}
		for _, ref := range []string{
			"turn-state", "tool-procedures", "implement-spawn",
			"skill-pointer", "skill-procedure",
			"worker-leg", "worker-task-assignment", "worker-task-preamble",
		} {
			if _, err := ir.Render(ctx, ref, nil); err == nil {
				t.Fatalf("InjectRenderer.Render accepted non-structural ref %q without Reader", ref)
			}
		}
		for _, key := range []string{"profile_has_command", "command_available", "recall_available", "can_sed_read"} {
			if _, err := gr.Render(ctx, "reject/_reject", map[string]any{key: true}); err == nil {
				t.Fatalf("GuidanceRenderer.Render accepted capability key %q in occurrence map", key)
			}
			if _, err := ir.Render(ctx, "scan-guidance-ephemeral", map[string]any{key: true}); err == nil {
				t.Fatalf("InjectRenderer.Render accepted capability key %q in occurrence map", key)
			}
		}
		if _, err := gr.RenderWithReach(ctx, toolreach.Request{}, "utility/command-output-omitted", nil); err == nil {
			t.Fatal("GuidanceRenderer.RenderWithReach accepted unknown zero toolreach.Request")
		}

		// Genuine occurrence values (counts, saved paths) remain usable on structural and reach-gated routes.
		discOut, err := gr.Render(ctx, "utility/source-remove-permanent", map[string]any{
			"path": "tmp/out", "size": "12 KB", "files": 14,
			"content_bytes": 12288, "recovery_bytes": 12288, "recovery_size": "12 KB", "recovery_known": true,
		})
		testutil.FailErr(t, "GuidanceRenderer.Render structural source-remove-permanent", err)
		if !strings.Contains(discOut, "14 files") || !strings.Contains(discOut, "tmp/out") {
			t.Fatalf("structural source-remove-permanent dropped count or path occurrence: %q", discOut)
		}
		reachOut, err := gr.RenderWithReach(ctx, toolreach.Offered([]string{"recall"}), "utility/command-output-omitted", map[string]any{
			"omitted_lines": "120", "omitted_size": "4 KB", "output_path": "@output/run 1.stdout",
			"quoted_output_path": "@output/'run 1.stdout'", "next_line": 10, "end_line": 50,
			"total_lines": 200, "total_bytes": int64(8192), "complete": true,
		})
		testutil.FailErr(t, "GuidanceRenderer.RenderWithReach command-output-omitted", err)
		if !strings.Contains(reachOut, "@output/run 1.stdout") || strings.Contains(reachOut, "sed -n") {
			t.Fatalf("RenderWithReach did not preserve saved path or leaked sed without command: %q", reachOut)
		}

		// Seeded actionable instruction in a transitively included partial fails closed on structural Render.
		seededEngine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
		testutil.FailErr(t, "register seeded structural guidance", seededEngine.Register("guidance/survey/jq-zoom.md", "Count {{ count }}. {% include \"partials/seeded-action.md\" %}"))
		testutil.FailErr(t, "register seeded partial", seededEngine.Register("partials/seeded-action.md", "Inspect with (sed -n '1,10p' {{ path }})."))
		if _, err := prompts.NewGuidanceRenderer(seededEngine).Render(ctx, "survey/jq-zoom", map[string]any{"count": 3}); err == nil {
			t.Fatal("GuidanceRenderer.Render accepted seeded transitive actionable partial in structural fragment")
		}
	})

	t.Run("zero-tool utility renderer and vocabulary guard enforce allowlist and zero capability claims", func(t *testing.T) {
		if _, err := ur.Render(ctx, "board-orientation", nil); err == nil {
			t.Fatal("UtilityPromptRenderer.Render accepted non-allowlisted model-facing ref")
		}
		if _, err := ur.Render(ctx, "utility/session-title-system", map[string]any{"profile_has_command": true}); err == nil {
			t.Fatal("UtilityPromptRenderer.Render accepted smuggled reader fact")
		}
		out, err := ur.Render(ctx, "utility/session-title-system", nil)
		testutil.FailErr(t, "UtilityPromptRenderer.Render allowlisted ref", err)
		if strings.TrimSpace(out) == "" {
			t.Fatal("UtilityPromptRenderer.Render returned empty output for session-title-system")
		}

		eff, err := extpacks.CatalogForConsumers()
		testutil.FailErr(t, "CatalogForConsumers", err)

		withFact := *eff
		withFact.Loaded = maps.Clone(eff.Loaded)
		withFact.Loaded["guidance/utility/session-title-system"] = extpacks.UnitEffective{
			ID:           "guidance/utility/session-title-system",
			Kind:         "guidance",
			WinnerPackID: "painted-wolf/platform",
			Content:      []byte("{% if profile_has_command %}Title{% endif %}"),
		}
		if _, err := prompts.CheckTemplateVocabulary(&withFact); err == nil {
			t.Fatal("CheckTemplateVocabulary accepted reader capability fact in zero-tool utility template")
		}

		withToolClaim := *eff
		withToolClaim.Loaded = maps.Clone(eff.Loaded)
		withToolClaim.Loaded["guidance/utility/session-title-system"] = extpacks.UnitEffective{
			ID:           "guidance/utility/session-title-system",
			Kind:         "guidance",
			WinnerPackID: "painted-wolf/platform",
			Content:      []byte("Summarize the session and call `command` to inspect files."),
		}
		if _, err := prompts.CheckTemplateVocabulary(&withToolClaim); err == nil {
			t.Fatal("CheckTemplateVocabulary accepted callable tool claim in zero-tool utility template")
		}
	})

	t.Run("internal call sites separate reader, utility, and user-notice renderers", assertInternalRendererCallSites)
}

func assertInternalRendererCallSites(t *testing.T) {
	t.Helper()
	internalRoot := filepath.Clean(filepath.Join(".."))
	fset := token.NewFileSet()
	err := filepath.WalkDir(internalRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(internalRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "ExecuteDetached":
				if !strings.HasPrefix(rel, "pongoplain/") && !strings.HasPrefix(rel, "usernotice/") && !strings.HasPrefix(rel, "toolvocab/") {
					t.Errorf("%s calls ExecuteDetached outside usernotice/toolvocab", rel)
				}
			case "RenderInject":
				if !strings.HasPrefix(rel, "prompts/") {
					t.Errorf("%s calls RenderInject outside internal/prompts", rel)
				}
			case "RenderUtilityPrompt":
				if len(call.Args) >= 2 {
					if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if ref, err := strconv.Unquote(lit.Value); err == nil && !prompts.IsZeroToolUtilityTemplate(ref) {
							t.Errorf("%s calls RenderUtilityPrompt with non-allowlisted literal %q", rel, ref)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	testutil.FailErr(t, "walk internal call sites", err)
}
