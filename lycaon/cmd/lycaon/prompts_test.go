package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPromptPreviewTypedVariables(t *testing.T) {
	flags, err := parsePromptsRenderFlags([]string{
		"guidance/tool-procedures", "--var", "profile_has_verify=true",
		"--var", "profile_has_command=false", "--var", "count=3",
		"--var", `offered_tools=["verify"]`, "--var", `literal="false"`,
		"--var", "label=plain text", "--var", `row={"name":"verify"}`,
		"--var", "missing=null",
	})
	testutil.FailErr(t, "parse preview flags", err)
	want := map[string]any{
		"profile_has_verify": true, "profile_has_command": false, "count": float64(3),
		"offered_tools": []any{"verify"}, "literal": "false", "label": "plain text",
		"row": map[string]any{"name": "verify"}, "missing": nil,
	}
	if !reflect.DeepEqual(flags.vars, want) {
		t.Fatalf("typed vars = %#v, want %#v", flags.vars, want)
	}
	result, err := prompts.AuthorRender(t.Context(), prompts.AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(), TemplateRef: flags.ref, Vars: flags.vars,
	})
	testutil.FailErr(t, "render typed preview", err)
	if !strings.Contains(result.Output, "VERIFY_UNVERIFIABLE") {
		t.Fatalf("preview did not select verify-only procedures: %s", result.Output)
	}
	flags.vars["profile_has_verify"] = false
	result, err = prompts.AuthorRender(t.Context(), prompts.AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(), TemplateRef: flags.ref, Vars: flags.vars,
	})
	testutil.FailErr(t, "render disabled preview", err)
	if strings.TrimSpace(result.Output) != "" {
		t.Fatalf("false flags must disable procedures: %s", result.Output)
	}
}

func TestPromptRenderSkillFlags(t *testing.T) {
	flags, err := parsePromptsRenderFlags([]string{
		"--skill", "write-a-skill", "--resource", "references/FORMAT.md",
		"--surface", "implement_routing", "--check",
	})
	testutil.FailErr(t, "parse skill flags", err)
	if flags.skillName != "write-a-skill" || flags.resource != "references/FORMAT.md" ||
		flags.surfaceID != "implement_routing" || !flags.check || flags.ref != "" {
		t.Fatalf("flags = %#v", flags)
	}
	result, err := prompts.AuthorRender(t.Context(), prompts.AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(), SkillName: flags.skillName,
		Resource: flags.resource, SurfaceID: flags.surfaceID, Check: flags.check,
	})
	testutil.FailErr(t, "render skill resource", err)
	if !strings.Contains(result.Output, "SKILL.md field table") {
		t.Fatalf("resource preview did not render: %s", result.Output)
	}
}

func TestPromptRenderSkillFlagExclusivity(t *testing.T) {
	if _, err := parsePromptsRenderFlags([]string{"guidance/tool-procedures", "--skill", "write-a-skill"}); err == nil {
		t.Fatal("a template ref and --skill must be mutually exclusive")
	}
	if _, err := parsePromptsRenderFlags([]string{"--resource", "references/FORMAT.md"}); err == nil {
		t.Fatal("--resource without --skill must fail")
	}
	if _, err := parsePromptsRenderFlags([]string{"--surface", "implement_routing"}); err == nil {
		t.Fatal("--surface without --skill must fail")
	}
}
