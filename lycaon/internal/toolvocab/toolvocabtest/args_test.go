package toolvocabtest_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
)

// An argument is read only where text attributes it to a known tool, and is
// checked against that tool's schema alone: a parameter the schema dropped or
// never had is caught, and the tool's own parameter passes.
func TestUnacceptedArgsChecksArgumentsAttributedToATool(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, argProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	// source_history's schema as it stands once a timeout_ms parameter is
	// dropped; recall reports none.
	schemas := map[string]map[string]any{
		"source_history": {"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}}},
	}
	schema := func(tool string) (map[string]any, bool) {
		s, ok := schemas[tool]
		return s, ok
	}
	for name, tc := range map[string]struct {
		text string
		want []toolvocabtest.ArgClaim
	}{
		"call with an unknown argument": {
			text: "Call `source_history(path=\"a.go\", wait_ms=500)` first.",
			want: []toolvocabtest.ArgClaim{{Tool: "source_history", Arg: "wait_ms"}},
		},
		"colon form and dotted path": {
			text: "Pass `source_history(depth: 2)`; `source_history.since` is ignored.",
			want: []toolvocabtest.ArgClaim{{Tool: "source_history", Arg: "depth"}, {Tool: "source_history", Arg: "since"}},
		},
		"accepted arguments":             {text: "Use `source_history(path=\"a=b\", mode=diff)` or `source_history.path`."},
		"unknown tool is not attributed": {text: "Call `frobnicate(wait_ms=1)`."},
		"dropped argument": {
			text: "Call `source_history(path=\"a.go\", timeout_ms=500)`; raise `source_history.timeout_ms` when slow.",
			want: []toolvocabtest.ArgClaim{{Tool: "source_history", Arg: "timeout_ms"}},
		},
		"own argument":                   {text: "Call `source_history(mode=diff)`."},
		"tool without a reported schema": {text: "Call `recall(wait_ms=1)`."},
		"prose parentheses":              {text: "The history (see source_history) has no arguments here."},
	} {
		t.Run(name, func(t *testing.T) {
			if got := toolvocabtest.UnacceptedArgs(catalog, tc.text, schema); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("UnacceptedArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAttributedCallValidationCatchesNestedEnumAndCrossToolErrors(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog for consumers", err)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	testutil.FailErr(t, "load profiles", err)
	rawSchemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "load schemas", err)
	catalog, err := toolvocab.NewCatalog(rawSchemas, profiles, toolvocab.Provenance{})
	testutil.FailErr(t, "new catalog", err)
	prunable, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), eff)
	testutil.FailErr(t, "discover prunable", err)

	baseSchema := func(name string) map[string]any {
		m, ok := rawSchemas.ToolMeta(name)
		if !ok {
			t.Fatalf("missing schema %q", name)
		}
		return tools.ProjectProviderToolMeta(tools.ToolMeta{Name: name, ArgsSchema: m.ArgsSchema}).ArgsSchema
	}

	readOnlyCommand := tools.PruneCommandReadOnlySchema(baseSchema("command"))
	askMetas := promptloop.ProjectAskToolMetas([]tools.ToolMeta{{Name: "ask_user", ArgsSchema: baseSchema("ask_user")}})
	askUserNarrowed := askMetas[0].ArgsSchema
	webWait := baseSchema("wait")
	webWait["properties"].(map[string]any)["conditions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["enum"] = []any{"http_ready", "port_ready"}

	projected := map[string]map[string]any{
		"command":      readOnlyCommand,
		"ask_user":     askUserNarrowed,
		"wait":         webWait,
		"complete_leg": baseSchema("complete_leg"),
		"verify":       baseSchema("verify"),
		"page_open":    baseSchema("page_open"),
		"survey_repo":  baseSchema("survey_repo"),
	}
	offered := map[string]bool{
		"command": true, "ask_user": true, "wait": true,
		"complete_leg": true, "verify": true, "page_open": true, "survey_repo": true,
	}
	lookup := func(tool string) (map[string]any, bool) {
		s, ok := projected[tool]
		return s, ok
	}

	failingCases := []struct {
		name string
		text string
	}{
		{
			name: "nested wait enum narrowed in complete call",
			text: "Park with `wait(until_complete=true, conditions=[{\"kind\":\"process_done\",\"handles\":[\"<handle>\"]}])`.",
		},
		{
			name: "nested wait enum narrowed in partial call",
			text: "Park with `wait(conditions=[{\"kind\":\"process_done\"}], …)`.",
		},
		{
			name: "standalone wait kind object narrowed",
			text: "Resume on `{\"kind\": \"process_done\"}`.",
		},
		{
			name: "ask_user narrowed secret enum in partial call",
			text: "Request key via `ask_user(response_type=\"secret\", …)`.",
		},
		{
			name: "ask_user pruned secret object in JSON call",
			text: "Request key via `ask_user({\"prompt\":\"Key?\",\"secret\":{\"name\":\"api_key\"}})`.",
		},
		{
			name: "standalone ask_user response_type secret in Ask mode",
			text: "Call `ask_user` with `response_type: secret`.",
		},
		{
			name: "conflicting verification boolean on complete_leg",
			text: "Finish with `complete_leg(leg_status=\"complete\", brief=\"done\", verification=true)`.",
		},
		{
			name: "standalone verification true not excused by complete_leg when command is read-only",
			text: "Run the check with `verification: true`.",
		},
		{
			name: "cross-tool secret_use attributed to read-only command not excused by verify",
			text: "Run `command` with `secret_use.services`.",
		},
		{
			name: "cross-tool capability_request attributed to page_open rejects local_listen",
			text: "Open with `page_open(url=\"http://127.0.0.1:3000\", capability_request={\"local_listen\":{\"ports\":[3000]}})`.",
		},
		{
			name: "cross-tool path argument on ask_user not excused by survey_repo or page_open",
			text: "Ask via `ask_user(prompt=\"Check\", path=\"src/main.go\")`.",
		},
		{
			name: "complete call missing required field",
			text: "Ask via `ask_user({\"response_type\":\"text\"})`.",
		},
		{
			name: "affirmative without phrase does not excuse pruned argument",
			text: "Run `command` with `verification: true` without delay.",
		},
		{
			name: "compound sentence negated verify does not excuse pruned secret_use on read-only command",
			text: "Do not call `verify`, and run `command` with `secret_use.services`.",
		},
	}
	for _, tc := range failingCases {
		t.Run(tc.name, func(t *testing.T) {
			issues := toolvocabtest.ValidateAttributedClaims(catalog, prunable, tc.text, offered, lookup)
			if len(issues) == 0 {
				t.Fatalf("expected schema validation failure for %q, got none", tc.text)
			}
		})
	}

	passingCases := []struct {
		name string
		text string
	}{
		{
			name: "admitted wait http_ready condition with placeholder",
			text: "Park with `wait(timeout_ms=30000, conditions=[{\"kind\":\"http_ready\",\"url\":\"http://127.0.0.1:8080/health\"}])`.",
		},
		{
			name: "verify accepts secret_use.services even when command is read-only",
			text: "Run `verify` with `secret_use.services`.",
		},
		{
			name: "complete_leg accepts verification object",
			text: "Finish via `complete_leg(leg_status=\"complete\", brief=\"done\", verification={\"method\":\"targeted\",\"reason\":\"unit tests passed\"})`.",
		},
		{
			name: "ask_user text response in Ask mode",
			text: "Clarify with `ask_user(prompt=\"Which target?\", response_type=\"text\")`.",
		},
		{
			name: "prohibition mentioning pruned argument is exempt",
			text: "Do not pass `capability_request.local_listen` on `command` here.",
		},
		{
			name: "without directly governing pruned argument is exempt",
			text: "Run `command` without `verification: true`.",
		},
	}
	for _, tc := range passingCases {
		t.Run(tc.name, func(t *testing.T) {
			if issues := toolvocabtest.ValidateAttributedClaims(catalog, prunable, tc.text, offered, lookup); len(issues) > 0 {
				t.Fatalf("unexpected validation issues for %q: %+v", tc.text, issues)
			}
		})
	}
}

func argProfiles() []sandbox.ToolProfile {
	return []sandbox.ToolProfile{
		{ID: "explore_readonly", Tools: map[string]bool{"recall": true, "source_history": true}},
		{ID: "acme_narrow", Tools: map[string]bool{"recall": true}},
	}
}
