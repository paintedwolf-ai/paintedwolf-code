package contract

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
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
// TestStockPackSkillsFrontmatterSchema requires every stock SKILL.md
// frontmatter to carry only documented top-level keys (INV-SK-02): the
// retired optional-tool frontmatter key must not return.
func TestStockPackSkillsFrontmatterSchema(t *testing.T) {
	// The test package lives at <module>/test/contract/prompts; resolve the
	// module root by walking up to go.mod so the walk works from any checkout.
	moduleRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Fatalf("module root %s does not contain go.mod: %v", moduleRoot, err)
	}
	root := filepath.Join(moduleRoot, "config", "packs")
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "SKILL.md" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk stock packs: %v", err)
	}
	if len(files) < 60 {
		t.Fatalf("expected the stock packs' ~61 SKILL.md files, found %d", len(files))
	}
	allowed := map[string]bool{
		"name": true, "description": true, "license": true,
		"compatibility": true, "metadata": true, "allowed-tools": true,
	}
	var failed []string
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: read: %v", path, err))
			continue
		}
		keys, err := frontmatterKeys(string(raw))
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: frontmatter: %v", path, err))
			continue
		}
		for _, key := range keys {
			if !allowed[key] {
				failed = append(failed, fmt.Sprintf("%s: undocumented frontmatter key %q", path, key))
			}
		}
	}
	if len(failed) > 0 {
		t.Fatalf("stock pack frontmatter schema violations (%d):\n  - %s", len(failed), strings.Join(failed, "\n  - "))
	}
}

// frontmatterKeys returns the sorted top-level YAML keys of a SKILL.md's
// frontmatter block.
func frontmatterKeys(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "---") {
		return nil, fmt.Errorf("missing frontmatter")
	}
	rest := trimmed[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(rest[:end]), &doc); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func stripVerbatimOutput(source, out string) string {
	for _, match := range verbatimPattern.FindAllStringSubmatch(source, -1) {
		out = strings.ReplaceAll(out, match[1], "")
	}
	return out
}

var verbatimPattern = regexp.MustCompile(`(?s)\{% verbatim %\}(.*?)\{% endverbatim %\}`)
