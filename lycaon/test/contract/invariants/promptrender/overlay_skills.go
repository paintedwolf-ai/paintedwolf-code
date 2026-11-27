package promptrender

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

var overlaySkillsMemo sync.Map

// overlaySkills loads the skills overlay's effective catalog admits, so a
// removed pack or disabled skill unit leaves the recipient's skill list.
func overlaySkills(s *Stock, overlay hoststate.PackOverlay) ([]skills.Skill, error) {
	if overlay.Catalog == nil {
		return StockSkills(s)
	}
	if cached, ok := overlaySkillsMemo.Load(overlay.Catalog.Revision); ok {
		return cached.([]skills.Skill), nil
	}
	loaded, diags := extpacks.LoadEffectiveSkills(overlay.Catalog)
	if len(diags) > 0 {
		messages := make([]string, 0, len(diags))
		for _, diag := range diags {
			messages = append(messages, diag.UnitID+": "+diag.Message)
		}
		return nil, fmt.Errorf("overlay %s skills carry diagnostics:\n%s", overlay.ID, strings.Join(messages, "\n"))
	}
	overlaySkillsMemo.Store(overlay.Catalog.Revision, loaded)
	return loaded, nil
}

// skillsKey identifies a skill list for memo keys.
func skillsKey(list []skills.Skill) string {
	names := make([]string, len(list))
	for i, sk := range list {
		names[i] = sk.Name
	}
	return strings.Join(names, ",")
}
