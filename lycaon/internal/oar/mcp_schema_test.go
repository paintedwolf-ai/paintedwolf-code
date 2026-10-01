package oar

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp/bindings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func bindingsTestdata(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "mcp", "bindings", "testdata", "mcp_bindings")
}

func TestNeedsMCPSchemaFacts(t *testing.T) {
	if NeedsMCPSchemaFacts(`tool == "read"`, nil) {
		t.Fatal("expected false")
	}
	if !NeedsMCPSchemaFacts(`mcp_schema_matched && mcp_field_bool("is_started")`, nil) {
		t.Fatal("expected true for schema refs")
	}
	if !NeedsMCPSchemaFacts(`mcp_has_field("issue_id")`, nil) {
		t.Fatal("expected true for mcp_has_field")
	}
}

func TestMCPFieldAccessors(t *testing.T) {
	list, err := bindings.LoadDir(bindingsTestdata(t))
	testutil.FailErr(t, "LoadDir", err)

	gc := NewGuardContext()
	gc.MCPProviderID = "fixture"
	gc.MCPToolName = "echo"
	gc.MCPResultText = `{"id":"ISSUE-1","count":3,"state":{"type":"started","name":"In Progress"}}`
	matched, fields, err := bindings.Apply(list, "fixture", "echo", gc.MCPResultText)
	testutil.FailErr(t, "Apply", err)
	if !matched {
		t.Fatal("expected match")
	}
	gc.MCPSchemaMatched = matched
	gc.MCPFields = fields

	ok, err := EvaluateCondition(`mcp_schema_matched && mcp_has_field("issue_id") && mcp_field_string("issue_id") == "ISSUE-1" && mcp_field_bool("is_started") && mcp_field_int("item_count") == 3`, gc)
	testutil.FailErr(t, "eval", err)
	if !ok {
		t.Fatal("expected fire")
	}
}

func TestLazyMCPSchemaApplyOnPost(t *testing.T) {
	list, err := bindings.LoadDir(bindingsTestdata(t))
	testutil.FailErr(t, "LoadDir", err)

	when := `mcp_schema_matched && mcp_field_string("issue_id") == "ISSUE-1"`
	rs := NewRuleSet([]*Rule{{
		OAR: "1.0", ID: "MCP_SCHEMA_TEST", Kind: KindPolicy,
		Anchor: AnchorToolPost, When: when,
		Effect: EffectWarn, Enforcement: "enforce", OnError: "fail_closed",
	}})
	pipeline := NewGuardPipeline(rs, nil, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolPost)
	pipeline.SetMCPBindingsFor(func(context.Context, string) []bindings.Binding { return list })

	calls := 0
	pipeline.SetMCPSchemaApply(func(providerID, toolName, resultText string) (bool, map[string]any, error) {
		calls++
		matched, fields, err := bindings.Apply(list, providerID, toolName, resultText)
		if fields == nil {
			return matched, nil, err
		}
		out := make(map[string]any, len(fields))
		for k, v := range fields {
			out[k] = v
		}
		return matched, out, err
	})

	gc := NewGuardContext()
	gc.Tool = "mcp_fixture_echo"
	gc.MCPProviderID = "fixture"
	gc.MCPToolName = "echo"
	gc.MCPResultText = `{"id":"ISSUE-1","count":1,"state":{"type":"started","name":"x"}}`

	res, err := pipeline.EvaluateBlock(context.Background(), AnchorToolPost, gc)
	testutil.FailErr(t, "EvaluateBlock", err)
	if calls != 1 {
		t.Fatalf("Apply calls=%d want 1", calls)
	}
	if !gc.MCPSchemaMatched {
		t.Fatal("expected schema matched")
	}
	if res == nil || res.Decision == nil {
		t.Fatal("expected decision")
	}
}

func TestLazyMCPSchemaSkippedWithoutRefs(t *testing.T) {
	list, err := bindings.LoadDir(bindingsTestdata(t))
	testutil.FailErr(t, "LoadDir", err)

	when := `mcp_provider_id == "fixture" && mcp_call_ok`
	rs := NewRuleSet([]*Rule{{
		OAR: "1.0", ID: "MCP_STRUCT_ONLY", Kind: KindPolicy,
		Anchor: AnchorToolPost, When: when,
		Effect: EffectWarn, Enforcement: "enforce", OnError: "fail_closed",
	}})
	pipeline := NewGuardPipeline(rs, nil, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolPost)
	pipeline.SetMCPBindingsFor(func(context.Context, string) []bindings.Binding { return list })

	calls := 0
	pipeline.SetMCPSchemaApply(func(providerID, toolName, resultText string) (bool, map[string]any, error) {
		calls++
		return bindings.Apply(list, providerID, toolName, resultText)
	})

	gc := NewGuardContext()
	gc.Tool = "mcp_fixture_echo"
	gc.MCPProviderID = "fixture"
	gc.MCPToolName = "echo"
	gc.MCPCallOK = true
	gc.MCPResultText = `{"id":"ISSUE-1","count":1,"state":{"type":"started","name":"x"}}`

	_, err = pipeline.EvaluateBlock(context.Background(), AnchorToolPost, gc)
	testutil.FailErr(t, "EvaluateBlock", err)
	if calls != 0 {
		t.Fatalf("Apply should be skipped, calls=%d", calls)
	}
	if gc.MCPSchemaMatched {
		t.Fatal("schema matched must stay false when skipped")
	}
}

func TestLazyMCPSchemaNeverOnPreInvoke(t *testing.T) {
	list, err := bindings.LoadDir(bindingsTestdata(t))
	testutil.FailErr(t, "LoadDir", err)

	when := `mcp_schema_matched`
	rs := NewRuleSet([]*Rule{{
		OAR: "1.0", ID: "MCP_PRE_SCHEMA", Kind: KindPolicy,
		Anchor: AnchorToolPreInvoke, When: when,
		Effect: EffectBlock, Enforcement: "enforce", OnError: "fail_closed",
	}})
	pipeline := NewGuardPipeline(rs, nil, NewCounterStore())
	pipeline.EnableAnchor(AnchorToolPreInvoke)
	pipeline.SetMCPBindingsFor(func(context.Context, string) []bindings.Binding { return list })

	calls := 0
	pipeline.SetMCPSchemaApply(func(providerID, toolName, resultText string) (bool, map[string]any, error) {
		calls++
		return true, map[string]any{"x": true}, nil
	})

	gc := NewGuardContext()
	gc.MCPProviderID = "fixture"
	gc.MCPToolName = "echo"
	gc.MCPResultText = `{"id":"x"}`

	_, err = pipeline.EvaluateBlock(context.Background(), AnchorToolPreInvoke, gc)
	testutil.FailErr(t, "EvaluateBlock", err)
	if calls != 0 {
		t.Fatalf("pre-invoke must not Apply, calls=%d", calls)
	}
}

func TestGoldenFixtureWidgetCondition(t *testing.T) {
	list, err := bindings.LoadDir(bindingsTestdata(t))
	testutil.FailErr(t, "LoadDir", err)
	matched, fields, err := bindings.Apply(list, "fixture", "widget", `{"status":"ready","count":2}`)
	testutil.FailErr(t, "Apply", err)
	if !matched {
		t.Fatal("expected match")
	}
	gc := NewGuardContext()
	gc.MCPSchemaMatched = matched
	gc.MCPFields = fields
	when := `mcp_schema_matched && mcp_field_bool("is_ready")`
	ok, err := EvaluateCondition(when, gc)
	testutil.FailErr(t, "eval", err)
	if !ok {
		t.Fatal("expected Decision 6-style true")
	}

	matchedBad, fieldsBad, err := bindings.Apply(list, "fixture", "widget", `{"nope":true}`)
	testutil.FailErr(t, "Apply bad", err)
	gc2 := NewGuardContext()
	gc2.MCPSchemaMatched = matchedBad
	gc2.MCPFields = fieldsBad
	ok, err = EvaluateCondition(when, gc2)
	testutil.FailErr(t, "eval bad", err)
	if ok {
		t.Fatal("wrong JSON must not fire")
	}
}
