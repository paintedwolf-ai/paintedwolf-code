package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
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

// TestPromptPartialsAreReachable verifies every partial has an include site.
func TestPromptPartialsAreReachable(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packDir := filepath.Join(root, packRoot)

	partials := map[string]string{} // "partials/x.md" -> repo-relative path
	var templates []string
	err := filepath.WalkDir(packDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		templates = append(templates, path)
		if dir := filepath.Base(filepath.Dir(path)); dir == "partials" {
			partials["partials/"+filepath.Base(path)] = path
		}
		return nil
	})
	contractcheck.FailErr(t, "walk pack templates", err)
	if len(partials) == 0 || len(templates) == 0 {
		t.Fatal("no partials or no templates found — the walk is broken")
	}

	referenced := map[string]bool{}
	for _, path := range templates {
		raw, readErr := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+path, readErr)
		for _, m := range pongoIncludeRE.FindAllStringSubmatch(string(raw), -1) {
			if len(m) > 1 {
				referenced[m[1]] = true
			}
		}
	}

	var violations []string
	for ref, path := range partials {
		if referenced[ref] {
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		contractcheck.FailErr(t, "relativize "+path, relErr)
		violations = append(violations, fmt.Sprintf(
			"%s has no include site — delete it, or include it where it belongs", filepath.ToSlash(rel)))
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "prompt partials nothing renders", contractcheck.DedupeStrings(violations))
}

// closeoutCitationPartials render together on synthesis prompts.
var closeoutCitationPartials = []string{
	packRoot + "/platform/shared/partials/coordinator-final-report.md",
	packRoot + "/platform/shared/partials/coordinator-synthesis-citation-grounding.md",
}

// TestCloseoutCopyMatchesTheReportDecoder verifies cited JSON keys.
func TestCloseoutCopyMatchesTheReportDecoder(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	accepted := map[string]bool{}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(guidance.CoordinatorCompletionReport{}),
		reflect.TypeOf(guidance.CoordinatorCitedEvidence{}),
	} {
		for i := range typ.NumField() {
			tag := typ.Field(i).Tag.Get("json")
			if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
				accepted[name] = true
			}
		}
	}
	if len(accepted) == 0 {
		t.Fatal("no json tags read off the closeout report — the reflection is broken")
	}

	// The keys a citation object may carry, as the copy spells them.
	citationKeys := []string{"evidence", "handle", "path", "line", "excerpt"}

	var violations []string
	for _, rel := range closeoutCitationPartials {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		contractcheck.FailErr(t, "read "+rel, err)
		text := string(raw)
		for _, key := range citationKeys {
			if accepted[key] {
				continue
			}
			for _, spelling := range []string{"`" + key + "`", `"` + key + `"`} {
				// Ignore explicit prohibitions of a key.
				scanned := strings.ReplaceAll(text, "Never use a "+spelling, "")
				if strings.Contains(scanned, spelling) {
					violations = append(violations, fmt.Sprintf(
						"%s names citation key %s, which the closeout decoder rejects", rel, spelling))
				}
			}
		}
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "closeout copy names a key the report decoder rejects", contractcheck.DedupeStrings(violations))
}

// progressClosurePartial names the tools that stay usable under the latch.
const progressClosurePartial = packRoot + "/platform/shared/partials/coordinator-progress-closure.md"

// TestProgressClosureCopyNamesOnlyUngatedTools verifies latch claims.
func TestProgressClosureCopyNamesOnlyUngatedTools(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), progressClosurePartial))
	contractcheck.FailErr(t, "read progress-closure partial", err)

	_, after, found := strings.Cut(string(raw), "never gated:")
	if !found {
		t.Fatal("progress-closure partial no longer states which tools stay usable — this test guards that sentence")
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
