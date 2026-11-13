package oar

import (
	"context"
	"fmt"
	"strings"

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
	if gc.MCP.MCPServerID == "" {
		return nil
	}
	if !rulesNeedMCPSchema(rules) {
		return nil
	}
	if gc.lazy.mcpSchemaComputed {
		return gc.lazy.mcpSchemaError
	}
	defer func() { gc.lazy.mcpSchemaComputed = true; gc.lazy.mcpSchemaError = err }()
	apply := p.mcpSchemaApply
	if apply == nil {
		list := p.mcpBindingsForSession(ctx, gc.Session.SessionID)
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
	matched, fields, err := apply(gc.MCP.MCPServerID, gc.MCP.MCPToolName, gc.MCP.MCPResultText)
	if err != nil {
		return err
	}
	gc.MCP.MCPSchemaMatched = matched
	gc.MCP.MCPFields = fields
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

// NeedsMCPSchemaFacts reports whether when/flow reads the schema match,
// mcp_result or one of its members, or an mcp_result accessor.
func NeedsMCPSchemaFacts(when string, flow []string) bool {
	needed := false
	add := func(name string) {
		switch {
		case name == "mcp_schema_matched", name == "mcp_result", name == "mcp_field_bool", name == "mcp_field_int":
			needed = true
		case strings.HasPrefix(name, "mcp_result."):
			needed = true
		}
	}
	scanIdents(when, add)
	for _, step := range flow {
		scanIdents(step, add)
	}
	return needed
}

// mcpResult is the mcp_result fact: the value an occurrence published, else
// the binding's projected fields.
func mcpResult(gc *GuardContext) map[string]any {
	if gc == nil {
		return nil
	}
	if published, ok := gc.Published["mcp_result"].(map[string]any); ok {
		return published
	}
	return gc.MCP.MCPFields
}

// mcpMember reads one typed member of mcp_result: the zero value when absent,
// and a provider failure when present with another type.
func mcpMember[T bool | int64](gc *GuardContext, key string) (any, error) {
	var zero T
	v, present := mcpResult(gc)[key]
	if !present {
		return zero, nil
	}
	if n, ok := v.(int); ok {
		v = int64(n)
	}
	typed, ok := v.(T)
	if !ok {
		return nil, fmt.Errorf("mcp_result member %q is %T, not %T", key, v, zero)
	}
	return typed, nil
}

func (p *GuardPipeline) mcpBindingsForSession(ctx context.Context, sessionID string) []bindings.Binding {
	if p == nil || p.mcpBindingsFor == nil {
		return nil
	}
	return p.mcpBindingsFor(ctx, sessionID)
}
