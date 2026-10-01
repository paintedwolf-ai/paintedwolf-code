package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// skillTriggerFixture is one skill's activation corpus.
type skillTriggerFixture struct {
	ShouldTrigger    []string `yaml:"should_trigger"`
	ShouldNotTrigger []string `yaml:"should_not_trigger"`
}

const (
	tiedFixtureFile   = "skill_trigger_fixtures.yaml"
	untiedFixtureFile = "tool_workflow_skill_trigger_fixtures.yaml"
)

// Fixture names are bundled skills; tied skills have complete entries; should_trigger prompts are unique.
func TestSkillTriggerFixturesCoverTheCatalog(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	loaded, _ := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(loaded) == 0 {
		t.Fatal("expected stock pack skills")
	}

	tied := map[string]bool{}
	known := map[string]bool{}
	for _, sk := range loaded {
		if sk.Project {
			continue
		}
		known[sk.Name] = true
		if strings.TrimSpace(sk.Metadata["host_resources"]) != "" {
			tied[sk.Name] = true
		}
	}

	tiedFixtures := loadSkillTriggerFixtures(t, tiedFixtureFile)
	untiedFixtures := loadSkillTriggerFixtures(t, untiedFixtureFile)

	// Tied skills gate on a probe; every one needs fixtures. The untied file is a subset.
	for name := range tied {
		if _, ok := tiedFixtures[name]; !ok {
			t.Errorf("%s: tied skill has no activation fixtures in %s", name, tiedFixtureFile)
		}
	}
	for name := range tiedFixtures {
		if !known[name] {
			t.Errorf("%s: fixture in %s names no bundled skill", name, tiedFixtureFile)
			continue
		}
		if !tied[name] {
			t.Errorf("%s: untied skill belongs in %s", name, untiedFixtureFile)
		}
	}
	for name := range untiedFixtures {
		if !known[name] {
			t.Errorf("%s: fixture in %s names no bundled skill", name, untiedFixtureFile)
			continue
		}
		if tied[name] {
			t.Errorf("%s: tied skill belongs in %s", name, tiedFixtureFile)
		}
	}

	// A should_trigger prompt may belong to only one skill.
	claimed := map[string]string{}
	for _, file := range []string{tiedFixtureFile, untiedFixtureFile} {
		fixtures := tiedFixtures
		if file == untiedFixtureFile {
			fixtures = untiedFixtures
		}
		for _, name := range sortedFixtureNames(fixtures) {
			fx := fixtures[name]
			if len(fx.ShouldTrigger) < 2 || len(fx.ShouldNotTrigger) < 1 {
				t.Errorf("%s (%s): needs at least 2 should_trigger and 1 should_not_trigger prompts", name, file)
			}
			for _, prompt := range fx.ShouldTrigger {
				if prior, dup := claimed[prompt]; dup {
					t.Errorf("prompt %q claimed by both %s and %s", prompt, prior, name)
					continue
				}
				claimed[prompt] = name
			}
		}
	}
}

func loadSkillTriggerFixtures(t *testing.T, file string) map[string]skillTriggerFixture {
	t.Helper()
	path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "test", "contract", "testdata", file)
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read "+file, err)
	var fixtures map[string]skillTriggerFixture
	contractcheck.FailErr(t, "parse "+file, yaml.Unmarshal(data, &fixtures))
	if len(fixtures) == 0 {
		t.Fatalf("%s: no fixtures parsed", file)
	}
	return fixtures
}

func sortedFixtureNames(fixtures map[string]skillTriggerFixture) []string {
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
