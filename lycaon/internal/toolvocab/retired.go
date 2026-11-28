package toolvocab

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// RetiredNames refuses profile grants and denies, rule selectors, and skill
// any_of_tools that name a retired tool. Each refusal is a
// *toolcontract.RetiredToolError carrying the recorded successor.
func RetiredNames(profiles []sandbox.ToolProfile, rules *oar.RuleSet, loaded []skills.Skill) error {
	var found []error
	check := func(site, name string) {
		if strings.Contains(name, "*") {
			return
		}
		if err := toolcontract.RetiredUse(site, name); err != nil {
			found = append(found, err)
		}
	}
	for _, p := range profiles {
		for name := range p.Tools {
			check(fmt.Sprintf("profile %q grant", p.ID), name)
		}
		for _, name := range p.DenyTools {
			check(fmt.Sprintf("profile %q deny", p.ID), name)
		}
	}
	if rules != nil {
		for _, rule := range rules.All() {
			for _, name := range rule.Tools() {
				check(fmt.Sprintf("rule %q selector.tool", rule.ID), name)
			}
		}
	}
	for _, skill := range loaded {
		for _, name := range skill.AnyOfTools() {
			check(fmt.Sprintf("skill %q any_of_tools", skill.Name), name)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Error() < found[j].Error() })
	return errors.Join(found...)
}
