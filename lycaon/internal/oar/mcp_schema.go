package oar

import (
	"context"

	"github.com/lycaon/lycaon/internal/mcp/bindings"
)

// MCPSchemaApplyFunc applies loaded bindings to an MCP result.
// Tests may wrap this to assert lazy skip.
type MCPSchemaApplyFunc func(providerID, toolName, resultText string) (matched bool, fields map[string]any, err error)

// MCPBindingsForFunc resolves the schema bindings for one evaluation from the
// catalog generation that session is pinned to.
type MCPBindingsForFunc func(ctx context.Context, sessionID string) []bindings.Binding

// SetMCPBindingsFor installs the per-evaluation bindings resolver, so a catalog
// swap reaches the next evaluation. Nil clears it.
func (p *GuardPipeline) SetMCPBindingsFor(fn MCPBindingsForFunc) {
	if p == nil {
		return
	}
	p.mcpBindingsFor = fn
	p.mcpSchemaApply = nil
}

// SetMCPSchemaApply installs a custom Apply (tests / spies). Nil restores
// default Apply over the resolved bindings.
func (p *GuardPipeline) SetMCPSchemaApply(fn MCPSchemaApplyFunc) {
	if p == nil {
		return
	}
	p.mcpSchemaApply = fn
}

func (p *GuardPipeline) maybeApplyMCPSchema(
	ctx context.Context, anchor string, rules []*Rule, gc *GuardContext,
) (err error) {
	if p == nil || gc == nil {
		return nil
	}
	if anchor != AnchorToolPost {
		return nil
	}
	if gc.MCPProviderID == "" {
		return nil
	}
	if !rulesNeedMCPSchema(rules) {
		return nil
	}
	if gc.mcpSchemaComputed {
		return gc.mcpSchemaError
	}
	defer func() { gc.mcpSchemaComputed = true; gc.mcpSchemaError = err }()
	apply := p.mcpSchemaApply
	if apply == nil {
		list := p.mcpBindingsForSession(ctx, gc.SessionID)
		if len(list) == 0 {
			return nil
		}
		apply = func(providerID, toolName, resultText string) (bool, map[string]any, error) {
			matched, fields, err := bindings.Apply(list, providerID, toolName, resultText)
			if fields == nil {
				return matched, nil, err
			}
			out := make(map[string]any, len(fields))
			for k, v := range fields {
				out[k] = v
			}
			return matched, out, err
		}
	}
	matched, fields, err := apply(gc.MCPProviderID, gc.MCPToolName, gc.MCPResultText)
	if err != nil {
		return err
	}
	gc.MCPSchemaMatched = matched
	gc.MCPFields = fields
	return nil
}

func rulesNeedMCPSchema(rules []*Rule) bool {
	for _, r := range rules {
		if r == nil {
			continue
		}
		for _, name := range RuleFactsReferenced(r) {
			if name == "mcp_schema_matched" || name == "mcp_has_field" || name == "mcp_field_bool" || name == "mcp_field_string" || name == "mcp_field_int" {
				return true
			}
		}
	}
	return false
}

// NeedsMCPSchemaFacts reports whether when/flow references schema variables or accessors.
func NeedsMCPSchemaFacts(when string, flow []string) bool {
	needed := false
	add := func(name string) {
		switch name {
		case "mcp_schema_matched", "mcp_has_field", "mcp_field_bool", "mcp_field_string", "mcp_field_int":
			needed = true
		}
	}
	scanIdents(when, add)
	for _, step := range flow {
		scanIdents(step, add)
	}
	return needed
}

// EvalMCPHasField is mcp_has_field(key).
func EvalMCPHasField(gc *GuardContext, key string) bool {
	if gc == nil || key == "" || gc.MCPFields == nil {
		return false
	}
	_, ok := gc.MCPFields[key]
	return ok
}

// EvalMCPFieldBool is mcp_field_bool(key); missing/wrong type → false.
func EvalMCPFieldBool(gc *GuardContext, key string) bool {
	if gc == nil || gc.MCPFields == nil {
		return false
	}
	v, ok := gc.MCPFields[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// EvalMCPFieldString is mcp_field_string(key); missing/wrong type → "".
func EvalMCPFieldString(gc *GuardContext, key string) string {
	if gc == nil || gc.MCPFields == nil {
		return ""
	}
	v, ok := gc.MCPFields[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// EvalMCPFieldInt is mcp_field_int(key); missing/wrong type → 0.
func EvalMCPFieldInt(gc *GuardContext, key string) int64 {
	if gc == nil || gc.MCPFields == nil {
		return 0
	}
	v, ok := gc.MCPFields[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func (p *GuardPipeline) mcpBindingsForSession(ctx context.Context, sessionID string) []bindings.Binding {
	if p == nil || p.mcpBindingsFor == nil {
		return nil
	}
	return p.mcpBindingsFor(ctx, sessionID)
}
