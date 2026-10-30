package contract

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Bundled skills teach the command form of every tool whose routine schema the
// command language replaces, in their bodies, resources, and optional_tools.
func TestBundledSkillsTeachCommandFormsForReplacedSchemas(t *testing.T) {
	fx := loadVocabularyFixture(t)
	language, err := toolcontract.LanguageV1()
	contractcheck.FailErr(t, "command language catalog", err)
	replaced := language.ReplacedRoutineSchemas()
	if len(replaced) == 0 {
		t.Fatal("expected replaced routine schemas in the command language catalog")
	}

	var failed []string
	for _, sk := range fx.skills {
		if sk.UserProvided {
			continue
		}
		texts := map[string]string{"SKILL.md": sk.Body}
		for _, resource := range sk.Resources {
			if !strings.HasSuffix(resource, ".md") {
				continue
			}
			data, err := sk.ReadResource(resource)
			contractcheck.FailErr(t, sk.Name+"/"+resource, err)
			texts[resource] = string(data)
		}
		for file, text := range texts {
			for _, tool := range toolvocab.NamedTools(fx.catalog, text) {
				if form, ok := replaced[tool]; ok {
					failed = append(failed, fmt.Sprintf("%s/%s names %q; teach the command form (%s)", sk.Name, file, tool, form))
				}
			}
		}
		for _, tool := range sk.OptionalTools {
			if form, ok := replaced[strings.TrimSpace(tool)]; ok {
				failed = append(failed, fmt.Sprintf("%s optional_tools lists %q; teach the command form (%s)", sk.Name, tool, form))
			}
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		t.Fatalf("bundled skills name replaced routine schemas (%d):\n  - %s", len(failed), strings.Join(failed, "\n  - "))
	}
}
