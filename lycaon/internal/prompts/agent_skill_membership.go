package prompts

import "strings"

const agentSkillMembershipPrefix = "agent_has_skill_"

// promptSkillMembershipKeys maps shipped prompt references to template keys
// without punctuation folding to prevent collisions with project skills.
var promptSkillMembershipKeys = map[string]string{
	"ask-for-a-decision":               agentSkillMembershipPrefix + "ask_for_a_decision",
	"craft-icons-and-chrome":           agentSkillMembershipPrefix + "craft_icons_and_chrome",
	"mock-before-build":                agentSkillMembershipPrefix + "mock_before_build",
	"research-current-information":     agentSkillMembershipPrefix + "research_current_information",
	"use-secrets-without-reading-them": agentSkillMembershipPrefix + "use_secrets_without_reading_them",
	"verify-terminal-change":           agentSkillMembershipPrefix + "verify_terminal_change",
	"verify-visual-change":             agentSkillMembershipPrefix + "verify_visual_change",
}

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
	return promptSkillMembershipKeys[name]
}
