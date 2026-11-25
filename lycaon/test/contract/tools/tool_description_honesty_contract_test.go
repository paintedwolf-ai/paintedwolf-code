package contract

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func collectSchemaVocabularyTokens(node any, dst map[string]string) {
	switch v := node.(type) {
	case map[string]any:
		if props, ok := v["properties"].(map[string]any); ok {
			for propName, propVal := range props {
				dst[propName] = "declared schema property"
				collectSchemaVocabularyTokens(propVal, dst)
			}
		}
		if enumVals, ok := v["enum"].([]any); ok {
			for _, ev := range enumVals {
				if s, ok := ev.(string); ok {
					dst[s] = "declared schema enum literal"
				}
			}
		}
		for k, child := range v {
			if k != "properties" && k != "enum" {
				collectSchemaVocabularyTokens(child, dst)
			}
		}
	case []any:
		for _, child := range v {
			collectSchemaVocabularyTokens(child, dst)
		}
	}
}

func baseNonToolDescriptionTokens(t *testing.T, metas []tools.ToolMeta) map[string]string {
	t.Helper()
	notATool := map[string]string{
		"workflow_id":        "field name in args, not a tool",
		"workflow_version":   "field name in args, not a tool",
		"phase_id":           "field name in args, not a tool",
		"blueprint_path":     "field name in args, not a tool",
		"detail_level":       "field name in args, not a tool",
		"template_id":        "field name in args, not a tool",
		"dry_run":            "field name in args, not a tool",
		"failed_leaves":      "JSON field name, not a tool",
		"already_advanced":   "JSON field name, not a tool",
		"phase_gate_unmet":   "error code, not a tool",
		"pending_user_input": "error code, not a tool",
		"current_phase":      "JSON field name, not a tool",
		"manifest":           "compose payload field name",
		"params":             "compose payload field name",
		"reason":             "input field on multiple tools",
		"value":              "scaffold-var update field",
		"path":               "scaffold-var update field",
		"paths":              "summarize input field",
		"content":            "summarize input field",
		"response":           "feedback resolve field",
		"comment":            "decision resolve field",
		"choice":             "decision resolve field",
		"network":            "command_output response field",
		"boundary_refusal":   "command_output response field",
		"secret_use":         "capability contract field",
		"true":               "boolean literal",
		"false":              "boolean literal",
	}
	for _, meta := range metas {
		collectSchemaVocabularyTokens(meta.ArgsSchema, notATool)
	}
	lang, err := toolcontract.ReadOnlyLanguageV1()
	contractcheck.FailErr(t, "read-only command language", err)
	for _, spec := range lang.Commands {
		notATool[spec.Name] = "declared read-only command program"
	}
	return notATool
}

// Every backticked snake_case token in registered and projected tool
// descriptions names a registered tool or a declared field, enum literal or
// command program. A model attempts any tool a description cites.
func TestToolDescriptionsCiteOnlyRegisteredTools(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	metas := reg.List()

	registered := map[string]bool{}
	for _, meta := range metas {
		registered[meta.Name] = true
	}
	if len(registered) == 0 {
		t.Fatal("serve boot registry lists no tools")
	}

	// snake_case token inside `…` backticks; minimum 2 chars to dodge
	// single-letter false positives.
	tokenRE := regexp.MustCompile("`([a-z][a-z0-9_]+)`")
	notATool := baseNonToolDescriptionTokens(t, metas)

	type violation struct {
		tool        string
		token       string
		description string
	}
	var found []violation
	resolved := 0
	checkProse := func(toolName, desc string) {
		if strings.TrimSpace(desc) == "" {
			return
		}
		for _, m := range tokenRE.FindAllStringSubmatch(desc, -1) {
			token := m[1]
			if registered[token] {
				resolved++
				continue
			}
			if _, ok := notATool[token]; ok {
				continue
			}
			found = append(found, violation{tool: toolName, token: token, description: desc})
		}
	}
	var walkSchema func(toolName string, node any)
	walkSchema = func(toolName string, node any) {
		switch v := node.(type) {
		case map[string]any:
			if desc, ok := v["description"].(string); ok {
				checkProse(toolName, desc)
			}
			for _, child := range v {
				walkSchema(toolName, child)
			}
		case []any:
			for _, child := range v {
				walkSchema(toolName, child)
			}
		}
	}

	// Static tool registry descriptions and parameter schemas.
	for _, meta := range metas {
		checkProse(meta.Name, meta.Description)
		walkSchema(meta.Name, meta.ArgsSchema)
	}

	// Projected surface descriptions across coordinator metadata.
	executor := toolexecution.NewExecutor(
		toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(contractcheck.ProdToolBoundary(t)), nil),
		reg,
		"coordinator",
	)
	projected, err := executor.Metadata.List(t.Context(), platform.ToolFilter{ProfileID: "coordinator"})
	contractcheck.FailErr(t, "projected coordinator tool metadata", err)
	for _, meta := range projected {
		checkProse("projected:"+meta.Name, meta.Description)
		walkSchema("projected:"+meta.Name, meta.ArgsSchema)
	}
	if len(found) > 0 {
		sort.Slice(found, func(i, j int) bool {
			if found[i].tool != found[j].tool {
				return found[i].tool < found[j].tool
			}
			return found[i].token < found[j].token
		})
		var b strings.Builder
		b.WriteString("tool descriptions reference snake_case tokens in backticks that are NOT registered tools.\n")
		b.WriteString("Either fix the typo, register the named tool, or add the token to notATool with a rationale.\n")
		for _, v := range found {
			b.WriteString("  ")
			b.WriteString(v.tool)
			b.WriteString(" → `")
			b.WriteString(v.token)
			b.WriteString("`  (description: ")
			b.WriteString(v.description)
			b.WriteString(")\n")
		}
		t.Fatal(b.String())
	}
}

// TestToolDescriptionsAreNotEmpty enforces a minimum bar on tool prose
// for every registered tool. An empty description means the model has no
// way to know when to call the tool.
func TestToolDescriptionsAreNotEmpty(t *testing.T) {
	reg := toolfixture.RegisterCatalogToolsForContract(t)
	var empty []string
	for _, meta := range reg.List() {
		if strings.TrimSpace(meta.Description) == "" {
			empty = append(empty, meta.Name)
		}
	}
	if len(empty) > 0 {
		sort.Strings(empty)
		t.Fatalf("tools registered without descriptions: %v\nThe model needs prose to decide when to call.", empty)
	}
	// A non-empty registry keeps the description assertions meaningful.
	if got := len(reg.List()); got == 0 {
		t.Fatal("tool registry produced zero metas — registration scaffolding likely broken")
	}
}
