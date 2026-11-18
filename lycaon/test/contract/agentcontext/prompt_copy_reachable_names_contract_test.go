package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/progress"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const packRoot = "lycaon/config/packs/painted-wolf"

// skillCatalogNames reads the shipped skills through the runtime's own loader.
func skillCatalogNames(t *testing.T) map[string]bool {
	t.Helper()
	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "resolve pack catalog", err)
	loaded, _ := extpacks.LoadEffectiveSkills(catalog)
	out := map[string]bool{}
	for _, skill := range loaded {
		if name := strings.TrimSpace(skill.Name); name != "" {
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("skill catalog is empty — the loader or the pack resolution is broken")
	}
	return out
}

// TestDeclaredSkillsResolveInTheCatalog verifies declared skill names.
func TestDeclaredSkillsResolveInTheCatalog(t *testing.T) {
	t.Parallel()
	known := skillCatalogNames(t)

	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "resolve pack catalog", err)
	defs, err := agentdef.LoadEffectiveWithCatalog(catalog)
	contractcheck.FailErr(t, "load agent definitions", err)

	var violations []string
	checked := 0
	for _, def := range defs {
		for label, sel := range map[string][]string{
			"skills": def.Skills.Names,
		} {
			for _, name := range sel {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				checked++
				if !known[name] {
					violations = append(violations, fmt.Sprintf(
						"agent %q %s names %q, which no pack ships", def.ID, label, name))
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no declared skill name was checked — the agent definitions or selectors are unreadable")
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "an agent selects a skill that does not exist", contractcheck.DedupeStrings(violations))
}

const progressClosureProcedure = packRoot + "/platform/shared/units/coordinator-progress-procedure.md"

// TestProgressClosureCopyNamesOnlyUngatedTools verifies latch claims.
func TestProgressClosureCopyNamesOnlyUngatedTools(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), progressClosureProcedure))
	contractcheck.FailErr(t, "read progress procedure", err)

	_, after, found := strings.Cut(string(raw), "never gated:")
	if !found {
		t.Fatal("progress procedure does not name the ungated lifecycle tools")
	}
	sentence, _, _ := strings.Cut(after, "\n")

	var named []string
	for _, m := range backtickedIdent.FindAllStringSubmatch(sentence, -1) {
		named = append(named, m[1])
	}
	if len(named) == 0 {
		t.Fatalf("no tool names read out of %q", sentence)
	}

	var violations []string
	for _, tool := range named {
		if progress.IsProgressGatedTool(tool) {
			violations = append(violations, fmt.Sprintf(
				"copy promises %q runs with the latch armed, but it is progress-gated", tool))
		}
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "progress-closure copy promises a tool the latch blocks", contractcheck.DedupeStrings(violations))
}
