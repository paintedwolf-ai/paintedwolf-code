package prompts

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/extpacks"
)

const agentSkillMembershipPrefix = "agent_has_skill_"

// stockSkillMembershipKeys maps stock pack skill names to template keys
// without punctuation folding, preventing collisions with project skills.
var stockSkillMembershipKeys = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	eff, err := extpacks.CatalogForConsumers()
	if err != nil || eff == nil {
		return out
	}
	for _, id := range eff.LoadedUnitIDs() {
		name, ok := strings.CutPrefix(id, "skills/")
		if !ok || name == "" {
			continue
		}
		if u, ok := eff.Loaded[id]; ok && eff.StockAuthority(u.WinnerPackID) {
			out[name] = agentSkillMembershipPrefix + strings.ReplaceAll(name, "-", "_")
		}
	}
	return out
})

func withAgentSkillMembershipVars(data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+8)
	for key, value := range data {
		if !strings.HasPrefix(key, agentSkillMembershipPrefix) {
			out[key] = value
		}
	}
	for _, skill := range agentSkillViewsFromVars(data) {
		if key := agentSkillMembershipKey(skill.Name); key != "" {
			out[key] = true
		}
	}
	return out
}

func agentSkillMembershipKey(name string) string {
	name = strings.TrimSpace(name)
	return stockSkillMembershipKeys()[name]
}
