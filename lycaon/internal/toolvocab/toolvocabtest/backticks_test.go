package toolvocabtest_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
)

func TestClosedBacktickVocabularyRejectsUnknownProtocolToken(t *testing.T) {
	vocab, err := toolvocabtest.NewBacktickVocab(nil)
	testutil.FailErr(t, "new backtick vocab", err)

	classified := map[string]toolvocabtest.BacktickKind{
		"command":                            toolvocabtest.BacktickToolOrOperation,
		"ask_user(prompt=\"Proceed?\")":      toolvocabtest.BacktickToolOrOperation,
		"cat -n FILE":                        toolvocabtest.BacktickCommandForm,
		"--max-time":                         toolvocabtest.BacktickCommandForm,
		"capability_request.host_resources":  toolvocabtest.BacktickSchemaToken,
		"next_worker_done":                   toolvocabtest.BacktickSchemaToken,
		"[read#3]":                           toolvocabtest.BacktickHandleOrRoot,
		"@scratch/out.json":                  toolvocabtest.BacktickHandleOrRoot,
		"Code: COORDINATOR_WORKER_IN_FLIGHT": toolvocabtest.BacktickCodeOrCatalogID,
		"implementer":                        toolvocabtest.BacktickCodeOrCatalogID,
		"## Scope":                           toolvocabtest.BacktickSyntaxLiteral,
	}
	for span, want := range classified {
		got, detail := vocab.Classify(span)
		if got != want {
			t.Fatalf("Classify(%q) = (%s, %s), want %s", span, got, detail, want)
		}
	}

	// Fenced code blocks illustrating target project code are ignored, while inline protocol tokens are checked.
	withFence := "Inspect with `command`.\n```go\nfunc unknownProjectHelper() { bogus_local_var := 1 }\n```\nLine five has `unknown_protocol_flag`."
	bad := vocab.CheckProse("agents/sample.md", withFence)
	if len(bad) != 1 || bad[0].Span != "unknown_protocol_flag" || bad[0].Line != 5 {
		t.Fatalf("expected single unclassified token at line 5, got %+v", bad)
	}
	diag := toolvocabtest.FormatBacktickDiagnostics("agents/sample.md", bad)
	if !strings.Contains(diag, "agents/sample.md:5:") || !strings.Contains(diag, "unknown_protocol_flag") {
		t.Fatalf("expected diagnostic with source, line, and token, got %q", diag)
	}

	// Authoring meta-documentation is respected when identified by path.
	if metaBad := vocab.CheckProse("config/packs/painted-wolf/platform/skills/write-a-skill/SKILL.md", "Example placeholder `my_custom_tool(foo=1)`."); len(metaBad) != 0 {
		t.Fatalf("expected authoring meta-doc exemption, got %+v", metaBad)
	}

	unclassifiedTokens := []string{
		"`frobnicate(path=\"a.go\")`",
		"`unknown_snake_protocol_token`",
		"`camelCaseProtocolToken`",
		"`capability_request.nonexistent_port`",
		"`BOGUS_REJECTION_CODE`",
		"`Code: FAKE_POLICY_CODE`",
		"`[bogus_kind#3]`",
		"`@undeclared_root_token/a.go`",
		"`nonexistent_param: true`",
	}
	for _, sample := range unclassifiedTokens {
		got := vocab.CheckProse("agents/test.md", "Use "+sample+" now.")
		if len(got) == 0 {
			t.Fatalf("expected %s to fail closed as unclassified", sample)
		}
	}

	// Calibrate against real rendered host personas and closeout guidance.
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog for consumers", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Catalog: eff})
	for _, agentID := range []string{"implementer", "path-explorer", "web-researcher"} {
		reader, rErr := prompts.DeclaredReader(nil, agentID)
		testutil.FailErr(t, "declared reader "+agentID, rErr)
		rendered, renErr := prompts.RenderPersona(t.Context(), engine, agentID, reader, spawn.PolicyTemplateVars())
		testutil.FailErr(t, "render persona "+agentID, renErr)
		if badSpans := vocab.CheckProse("persona/"+agentID, rendered); len(badSpans) > 0 {
			t.Fatalf("real rendered persona %s had unclassified backticks:\n%s", agentID, toolvocabtest.FormatBacktickDiagnostics("persona/"+agentID, badSpans))
		}
	}
}
