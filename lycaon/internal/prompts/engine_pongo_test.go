package prompts_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPongoEngineResolvesPartial(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "partials"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "partials", "child.md"), []byte("CHILD_BODY"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "parent.md"), []byte(`before {% include "partials/child.md" %} after`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	got, err := engine.Render(context.Background(), "parent.md", nil)
	testutil.FailErr(t, "render prompt template", err)
	if !strings.Contains(got, "CHILD_BODY") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "include") {
		t.Fatalf("unexpanded include in %q", got)
	}
}

func TestPongoEngineNestedInclude(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"partials", "nested"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "leaf.md"), []byte("LEAF"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "partials", "mid.md"), []byte(`{% include "nested/leaf.md" %}`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.md"), []byte(`{% include "partials/mid.md" %}`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	got, err := engine.Render(context.Background(), "root.md", nil)
	testutil.FailErr(t, "render prompt template", err)
	if strings.TrimSpace(got) != "LEAF" {
		t.Fatalf("got %q", got)
	}
}

func TestPongoRenderMissingIncludeFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(`{% include "missing.md" %}`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	_, err := engine.Render(context.Background(), "bad.md", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "unable to resolve") {
		t.Fatalf("err = %v", err)
	}
}

func TestPongoTemplateRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "escape.md"), []byte(`{% include "../outside.md" %}`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	_, err := engine.Render(context.Background(), "escape.md", nil)
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

// Includes use the same overlay order as entry templates.
func TestPongoOverlaySiteWinsEntryAndInclude(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{
		config.PlatformPrompts.Join("pongo-overlay-parent.md"): `{% include "partials/pongo-overlay-tone.md" %}`,
		config.SharedPartials.Join("pongo-overlay-tone.md"):    "bundled-partial",
	})
	site := t.TempDir()
	if err := os.MkdirAll(filepath.Join(site, "partials"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(site, "partials", "pongo-overlay-tone.md"), []byte("site-partial-wins"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	bare := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	bareGot, err := bare.Render(context.Background(), "agents/pongo-overlay-parent.md", nil)
	testutil.FailErr(t, "render prompt template", err)
	if !strings.Contains(bareGot, "bundled-partial") {
		t.Fatalf("bundled include = %q want bundled-partial", bareGot)
	}

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: site})
	got, err := engine.Render(context.Background(), "agents/pongo-overlay-parent.md", nil)
	testutil.FailErr(t, "render prompt template", err)
	if !strings.Contains(got, "site-partial-wins") {
		t.Fatalf("got %q want site overlay partial", got)
	}
	if strings.Contains(got, "bundled-partial") {
		t.Fatalf("got %q still carries the bundled partial", got)
	}
}

func TestPongoIncludeDepthSiblingIncludes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "partials"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "partials", "leaf.md"), []byte("LEAF"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	var includes strings.Builder
	for i := 0; i < 15; i++ {
		includes.WriteString(`{% include "partials/leaf.md" %}`)
	}
	if err := os.WriteFile(filepath.Join(dir, "parent.md"), []byte(includes.String()), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	got, err := engine.Render(context.Background(), "parent.md", nil)
	testutil.FailErr(t, "render prompt template", err)
	if !strings.Contains(got, "LEAF") {
		t.Fatalf("got %q", got)
	}
}

func TestPongoIncludeDepthExceeded(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= 13; i++ {
		name := fmt.Sprintf("d%d", i)
		var body string
		if i < 13 {
			next := fmt.Sprintf("d%d", i+1)
			body = `{% include "` + next + `.md" %}`
		} else {
			body = "leaf"
		}
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	_, err := engine.Render(context.Background(), "d0.md", nil)
	if err == nil {
		t.Fatal("expected depth error")
	}
	if !strings.Contains(err.Error(), "depth exceeds") && !strings.Contains(err.Error(), "unable to resolve") {
		t.Fatalf("err = %v", err)
	}
}

func TestPongoIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(`{% include "b.md" %}`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte(`{% include "a.md" %}`), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	_, err := engine.Render(context.Background(), "a.md", nil)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") && !strings.Contains(err.Error(), "unable to resolve") {
		t.Fatalf("err = %v", err)
	}
}

func TestPongoCompositionRecognizesTrimmedIncludeCycles(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write a", os.WriteFile(filepath.Join(dir, "a.md"), []byte(`{%-   include "b.md"   -%}`), 0o644))
	testutil.FailErr(t, "write b", os.WriteFile(filepath.Join(dir, "b.md"), []byte(`{% include "a.md" %}`), 0o644))
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	_, err := engine.Render(t.Context(), "a.md", nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("trimmed include cycle error = %v", err)
	}
}

func TestPongoCompositionRequiresStaticDependencies(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write dynamic", os.WriteFile(filepath.Join(dir, "dynamic.md"), []byte(`{% include target %}`), 0o644))
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: dir})
	_, err := engine.Render(t.Context(), "dynamic.md", map[string]any{"target": "outside.md"})
	if err == nil || !strings.Contains(err.Error(), "string literal") {
		t.Fatalf("dynamic dependency error = %v", err)
	}
}

func TestPongoOverlayRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	testutil.FailErr(t, "write secret", os.WriteFile(secret, []byte("SECRET-MUST-NOT-RENDER"), 0o600))
	testutil.FailErr(t, "link escape", os.Symlink(secret, filepath.Join(root, "escape.md")))
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: root})
	out, err := engine.Render(t.Context(), "escape.md", nil)
	if err == nil {
		t.Fatalf("symlink escape rendered %q", out)
	}
	if !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("symlink cause was masked: %v", err)
	}
	if strings.Contains(out, "SECRET-MUST-NOT-RENDER") {
		t.Fatalf("symlink escape leaked %q", out)
	}
}

func TestPongoRenderPreservesAuthoredWhitespace(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	testutil.FailErr(t, "register", engine.Register("spacing.md", "one\n\n\n\nthree"))
	out, err := engine.Render(t.Context(), "spacing.md", nil)
	testutil.FailErr(t, "render", err)
	if out != "one\n\n\n\nthree" {
		t.Fatalf("whitespace changed: %q", out)
	}
}

func TestPongoRegisterInMemory(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("mem.md", `hello {{ focus }}`); err != nil {
		testutil.FailErr(t, "engine.Register failed", err)
	}
	got, err := engine.Render(context.Background(), "mem.md", map[string]any{"focus": "there", "ignored": "x"})
	testutil.FailErr(t, "render prompt template", err)
	if got != "hello there" {
		t.Fatalf("got %q", got)
	}
}

// Every template receives its caller's data.
func TestPongoRenderPassesCallerVarsForEveryRef(t *testing.T) {
	for name, ref := range map[string]string{
		"persona":   "agents/implementer.md",
		"partial":   "partials/native-read-tool.md",
		"archetype": "archetypes/explore_readonly.md",
		"inject":    "inject/test.md",
		"guidance":  "guidance/test.md",
		"kick":      "kicks/test.md",
		"bare":      "x.md",
	} {
		t.Run(name, func(t *testing.T) {
			engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
			if err := engine.Register(ref, `{{ role_title }}|{% if profile_has_grep %}{{ max_tool_loops }}{% endif %}`); err != nil {
				testutil.FailErr(t, "engine.Register failed", err)
			}
			got, err := engine.Render(context.Background(), ref, map[string]any{
				"role_title":       "ok",
				"profile_has_grep": true,
				"max_tool_loops":   7,
			})
			testutil.FailErr(t, "render prompt template", err)
			if got != "ok|7" {
				t.Fatalf("%s: got %q, want the caller's vars to reach the template", ref, got)
			}
		})
	}
}

// Missing values remain false in optional branches.
func TestPongoRenderLeavesUnsuppliedVarsFalsy(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("x.md", `{% if absent %}yes{% else %}no{% endif %}`); err != nil {
		testutil.FailErr(t, "engine.Register failed", err)
	}
	got, err := engine.Render(context.Background(), "x.md", map[string]any{})
	testutil.FailErr(t, "render prompt template", err)
	if got != "no" {
		t.Fatalf("got %q want the else branch", got)
	}
}
