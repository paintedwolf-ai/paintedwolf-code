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
