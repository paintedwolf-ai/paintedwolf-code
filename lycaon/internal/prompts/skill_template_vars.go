package prompts

import (
	"github.com/lycaon/lycaon/internal/spawn"
)

// SkillVars builds the template variables one skill read renders with from the
// reader's facts plus spawn policy and sizing.
func SkillVars(reader Reader) (map[string]any, error) {
	occ := spawn.PolicyTemplateVars()
	spawn.RefreshSizingHintVars(occ)
	return Vars(reader, occ)
}
