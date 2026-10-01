package contract

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/skills"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Bundled descriptions must fit IndexDescriptionBudget and stay what+when copy.
func TestBundledSkillDescriptionsStayIndexSized(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	loaded, _ := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(loaded) == 0 {
		t.Fatal("expected stock pack skills")
	}

	var failed []string
	for _, sk := range loaded {
		if sk.Project {
			continue
		}
		desc := strings.TrimSpace(sk.Description)
		n := utf8.RuneCountInString(desc)
		if n == 0 {
			failed = append(failed, sk.Name+": empty description")
			continue
		}
		if n > skills.RosterDescriptionBudget {
			failed = append(failed, sk.Name+": description length "+strconv.Itoa(n)+" exceeds RosterDescriptionBudget")
		}
		lower := strings.ToLower(desc)
		if strings.Contains(lower, "read this skill") {
			failed = append(failed, sk.Name+": description contains \"read this skill\" (belongs in the body)")
		}
		if strings.Contains(desc, "Do not") || strings.Contains(desc, "do not") {
			failed = append(failed, sk.Name+": description contains \"Do not\" (belongs in the body)")
		}
	}
	if len(failed) > 0 {
		t.Fatalf("bundled skill description shape failed (%d):\n  - %s", len(failed), strings.Join(failed, "\n  - "))
	}
}
