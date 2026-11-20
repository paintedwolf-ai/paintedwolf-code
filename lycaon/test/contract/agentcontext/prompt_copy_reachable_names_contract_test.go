package contract

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/extpacks"
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
