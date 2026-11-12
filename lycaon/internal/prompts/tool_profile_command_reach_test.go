package prompts_test

import "testing"

func TestToolProfileCapabilityVarsFollowCommandReach(t *testing.T) {
	t.Parallel()
	// Read-only command leg: dual-state read forms reachable, mutating
	// forms and replace-routine mutation commands not.
	readonly := loadProfileCapabilityVars(t, "explore_readonly")
	for _, key := range []string{"profile_has_summarize", "profile_has_survey_repo"} {
		if readonly[key] != true {
			t.Errorf("explore_readonly %s = %v, want true", key, readonly[key])
		}
	}
	for _, key := range []string{"profile_has_code_rewrite", "profile_has_jq_edit"} {
		if readonly[key] != false {
			t.Errorf("explore_readonly %s = %v, want false", key, readonly[key])
		}
	}
	// Mutating command leg with the form tools held: code-rewrite reachable.
	implement := loadProfileCapabilityVars(t, "implement")
	for _, key := range []string{"profile_has_code_rewrite", "profile_has_jq_edit"} {
		if implement[key] != true {
			t.Errorf("implement %s = %v, want true", key, implement[key])
		}
	}
}
