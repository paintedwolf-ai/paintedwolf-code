package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestStockPackSkillsRenderWithPolicyVars requires every pack SKILL.md body to
// execute under default policy vars.
func TestStockPackSkillsRenderWithPolicyVars(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	loaded, _ := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(loaded) == 0 {
		t.Fatal("expected stock pack skills")
	}
	vars := spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
	var failed []string
	for _, sk := range loaded {
		if sk.Project {
			continue
		}
		for _, resource := range sk.TemplateResources() {
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
			if _, err := pongoplain.Execute(t.Context(), tpl, vars); err != nil {
				failed = append(failed, fmt.Sprintf("%s/%s: execute: %v", sk.Name, resource, err))
			}
		}
		tpl, perr := pongoplain.Compile(sk.Body)
		if perr != nil {
			failed = append(failed, fmt.Sprintf("%s: parse: %v", sk.Name, perr))
			continue
		}
		if _, xerr := pongoplain.Execute(t.Context(), tpl, vars); xerr != nil {
			failed = append(failed, fmt.Sprintf("%s: execute: %v", sk.Name, xerr))
		}
	}
	if len(failed) > 0 {
		t.Fatalf("pack skill pongo render failed (%d):\n  - %s", len(failed), strings.Join(failed, "\n  - "))
	}
}
