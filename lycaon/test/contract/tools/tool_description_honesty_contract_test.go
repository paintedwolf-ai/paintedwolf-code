package contract

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// TestToolDescriptionsCiteOnlyRegisteredTools: every backticked snake_case
// token in a tool description names a registered tool. A model attempts any
// tool a description cites, so a misspelled name becomes a failed call.
func TestToolDescriptionsCiteOnlyRegisteredTools(t *testing.T) {
	reg := toolfixture.RegisterCatalogToolsForContract(t)

	registered := map[string]bool{}
	for _, meta := range reg.List() {
		registered[meta.Name] = true
	}

	// snake_case token inside `…` backticks; minimum 2 chars to dodge
	// single-letter false positives.
	tokenRE := regexp.MustCompile("`([a-z][a-z0-9_]+)`")

	// Snake_case concepts that are not tools, each with its reason.
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
		"response":           "feedback resolve field",
		"comment":            "decision resolve field",
		"choice":             "decision resolve field",
	}

	type violation struct {
		tool        string
		token       string
		description string
	}
	var found []violation
	for _, meta := range reg.List() {
		desc := meta.Description
		if strings.TrimSpace(desc) == "" {
			continue
		}
		for _, m := range tokenRE.FindAllStringSubmatch(desc, -1) {
			token := m[1]
			if registered[token] {
				continue
			}
			if _, ok := notATool[token]; ok {
				continue
			}
			found = append(found, violation{tool: meta.Name, token: token, description: desc})
		}
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
