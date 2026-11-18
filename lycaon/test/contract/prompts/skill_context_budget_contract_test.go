package contract

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/skills"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every bundled skill loads, its description fits the skill index, and its body
// and resources fit the context a preload or resource read spends beside a small
// model's standing prompt.
func TestBundledSkillsLoadWithinContextBudgets(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	loaded, diags := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(loaded) == 0 {
		t.Fatal("expected stock pack skills")
	}

	var failed []string
	for _, d := range diags {
		failed = append(failed, fmt.Sprintf("%s: %s (%s)", d.UnitID, d.Message, d.Code))
	}
	for _, sk := range loaded {
		if sk.Project || sk.UserProvided {
			continue
		}
		switch n := utf8.RuneCountInString(strings.TrimSpace(sk.Description)); {
		case n == 0:
			failed = append(failed, fmt.Sprintf("%s: empty description", sk.Name))
		case n > skills.RosterDescriptionBudget:
			failed = append(failed, fmt.Sprintf("%s: description is %d runes; budget %d", sk.Name, n, skills.RosterDescriptionBudget))
		}
		if n := len(sk.Body); n > skills.BundledBodyBudget {
			failed = append(failed, fmt.Sprintf("%s: body is %d bytes; budget %d", sk.Name, n, skills.BundledBodyBudget))
		}
		if sk.ResourcesOmitted > 0 {
			failed = append(failed, fmt.Sprintf("%s: %d resources beyond the listing bound", sk.Name, sk.ResourcesOmitted))
		}
		for _, resource := range sk.Resources {
			data, err := sk.ReadResource(resource)
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s/%s: %v", sk.Name, resource, err))
				continue
			}
			if n := len(data); n > skills.BundledResourceBudget {
				failed = append(failed, fmt.Sprintf("%s/%s: %d bytes; budget %d", sk.Name, resource, n, skills.BundledResourceBudget))
			}
		}
	}
	if len(failed) > 0 {
		t.Fatalf("bundled skills fail to load or exceed context budgets (%d); move detail to references or split them:\n  - %s",
			len(failed), strings.Join(failed, "\n  - "))
	}
}
