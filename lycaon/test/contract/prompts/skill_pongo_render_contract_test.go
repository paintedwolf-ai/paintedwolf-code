package contract

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestStockPackSkillsRenderWithPolicyVars requires every pack SKILL.md body
// and every bundled references/*.md to execute under default policy vars,
// render fully (no leftover template markers), and stay within the resource
// budget.
func TestStockPackSkillsRenderWithPolicyVars(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	loaded, _ := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(loaded) == 0 {
		t.Fatal("expected stock pack skills")
	}
	vars := spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
	var failed []string
	references := 0
	for _, sk := range loaded {
		if sk.Project {
			continue
		}
		tpl, perr := pongoplain.Compile(sk.Body)
		if perr != nil {
			failed = append(failed, fmt.Sprintf("%s: parse: %v", sk.Name, perr))
			continue
		}
		if _, xerr := pongoplain.Execute(t.Context(), tpl, vars); xerr != nil {
			failed = append(failed, fmt.Sprintf("%s: execute: %v", sk.Name, xerr))
		}
		for _, resource := range sk.MarkdownResources() {
			references++
			body, err := sk.ReadResource(resource)
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s/%s: read: %v", sk.Name, resource, err))
				continue
			}
			tpl, err := pongoplain.Compile(string(body))
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s/%s: parse: %v", sk.Name, resource, err))
				continue
			}
			out, err := pongoplain.Execute(t.Context(), tpl, vars)
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s/%s: execute: %v", sk.Name, resource, err))
				continue
			}
			if len(out) > skills.BundledResourceBudget {
				failed = append(failed, fmt.Sprintf("%s/%s: rendered %d bytes exceeds budget %d", sk.Name, resource, len(out), skills.BundledResourceBudget))
			}
			// Markers may survive rendering only inside verbatim blocks.
			if stripped := stripVerbatimOutput(string(body), out); strings.Contains(stripped, "{{") || strings.Contains(stripped, "{%") {
				failed = append(failed, fmt.Sprintf("%s/%s: leftover template markers in rendered output", sk.Name, resource))
			}
		}
	}
	if references < 100 {
		failed = append(failed, fmt.Sprintf("expected the stock packs' ~112 references/*.md, walked only %d", references))
	}
	if len(failed) > 0 {
		t.Fatalf("pack skill pongo render failed (%d):\n  - %s", len(failed), strings.Join(failed, "\n  - "))
	}
}

// TestStockTerminalEvidenceKeepsSecretPlaceholder asserts the one intentional
// verbatim block: the secret handle placeholder survives rendering.
func TestStockTerminalEvidenceKeepsSecretPlaceholder(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	loaded, _ := extpacks.LoadEffectiveSkills(extpacks.Active())
	vars := spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
	found := false
	for _, sk := range loaded {
		if sk.Name != "verify-terminal-change" {
			continue
		}
		body, err := sk.ReadResource("references/terminal-evidence.md")
		if err != nil {
			t.Fatalf("read terminal-evidence.md: %v", err)
		}
		tpl, err := pongoplain.Compile(string(body))
		if err != nil {
			t.Fatalf("parse terminal-evidence.md: %v", err)
		}
		out, err := pongoplain.Execute(t.Context(), tpl, vars)
		if err != nil {
			t.Fatalf("execute terminal-evidence.md: %v", err)
		}
		if !strings.Contains(out, "{{paintedwolf-secret:") {
			t.Fatal("terminal-evidence.md lost the verbatim secret handle placeholder")
		}
		found = true
	}
	if !found {
		t.Fatal("verify-terminal-change skill not loaded")
	}
}

// stripVerbatimOutput removes verbatim-block contents (which rendering
// preserves literally) from rendered output, so the leftover-marker check
// only flags markers a template leaked outside verbatim protection.
func stripVerbatimOutput(source, out string) string {
	for _, match := range verbatimPattern.FindAllStringSubmatch(source, -1) {
		out = strings.ReplaceAll(out, match[1], "")
	}
	return out
}

var verbatimPattern = regexp.MustCompile(`(?s)\{% verbatim %\}(.*?)\{% endverbatim %\}`)
