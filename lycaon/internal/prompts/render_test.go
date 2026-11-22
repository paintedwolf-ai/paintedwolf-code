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
		for _, ref := range []string{"board-orientation", "command-jobs", "source-changes", "coordinator-iterations-low", "coordinator-spend-runway-low"} {
			if _, err := gr.Render(ctx, ref, nil); err == nil {
				t.Fatalf("GuidanceRenderer.Render accepted reader-conditioned ref %q without Reader", ref)
			}
		}
		for _, ref := range []string{"turn-state", "tool-procedures", "implement-spawn"} {
			if _, err := ir.Render(ctx, ref, nil); err == nil {
				t.Fatalf("InjectRenderer.Render accepted reader-conditioned ref %q without Reader", ref)
			}
		}
		if _, err := gr.Render(ctx, "reject/_reject", map[string]any{"profile_has_command": true}); err == nil {
			t.Fatal("GuidanceRenderer.Render accepted smuggled reader fact in occurrence map")
		}
		if _, err := ir.Render(ctx, "scan-guidance-ephemeral", map[string]any{"has_git": true}); err == nil {
			t.Fatal("InjectRenderer.Render accepted smuggled reader fact in occurrence map")
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

	t.Run("internal call sites separate reader, utility, and user-notice renderers", func(t *testing.T) {
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
	})
}
