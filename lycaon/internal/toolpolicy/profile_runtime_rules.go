package toolpolicy

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProfileRuntimeRulesConfig is the on-disk agent-tool-profiles.yaml shape.
type ProfileRuntimeRulesConfig struct {
	Profiles map[string]ProfileRuntimeRuleSet `yaml:"profiles"`
}

// ProfileRuntimeRuleSet is runtime_rules for one tool profile.
type ProfileRuntimeRuleSet struct {
	RuntimeRules []ProfileRuntimeRule `yaml:"runtime_rules"`
}

// ProfileRuntimeRule is one coordinator invoke guard.
type ProfileRuntimeRule struct {
	ID        string            `yaml:"id"`
	Tool      string            `yaml:"tool"`
	ArgEquals map[string]string `yaml:"arg_equals"`
	Code      string            `yaml:"code"`
}

// ProfileRuntimeRules evaluates bundled profile runtime_rules before posture rules.
type ProfileRuntimeRules struct {
	byProfile map[string][]ProfileRuntimeRule
}

// LoadProfileRuntimeRules reads config/packs/painted-wolf/platform/host/agent-tool-profiles.yaml.
func LoadProfileRuntimeRules() (*ProfileRuntimeRules, error) {
	data, err := config.Read(config.AgentToolProfiles)
	if err != nil {
		return nil, err
	}
	var cfg ProfileRuntimeRulesConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	out := &ProfileRuntimeRules{byProfile: make(map[string][]ProfileRuntimeRule)}
	for profileID, set := range cfg.Profiles {
		out.byProfile[strings.TrimSpace(profileID)] = append([]ProfileRuntimeRule(nil), set.RuntimeRules...)
	}
	return out, nil
}

// EvaluateCoordinator matches runtime_rules for the coordinator profile.
func (r *ProfileRuntimeRules) EvaluateCoordinator(
	sess *api.Session,
	toolName string,
	args map[string]any,
	rejectFmt *guidance.StaticRejectFormatter,
) error {
	if r == nil || !isCoordinatorInvokeSession(sess) {
		return nil
	}
	rules := r.byProfile["coordinator"]
	for _, rule := range rules {
		if !rule.matches(toolName, args) {
			continue
		}
		code := strings.TrimSpace(rule.Code)
		if code == "" {
			continue
		}
		if rejectFmt != nil {
			formatted, err := rejectFmt.Format(code, map[string]any{"tool": toolName})
			if err == nil {
				return fmt.Errorf("%s", formatted)
			}
		}
		return fmt.Errorf("%s: coordinator runtime rule %s", code, strings.TrimSpace(rule.ID))
	}
	return nil
}

func (rule ProfileRuntimeRule) matches(toolName string, args map[string]any) bool {
	if strings.TrimSpace(rule.Tool) != strings.TrimSpace(toolName) {
		return false
	}
	for key, want := range rule.ArgEquals {
		got, ok := args[key]
		if !ok {
			return false
		}
		if strings.TrimSpace(fmt.Sprint(got)) != strings.TrimSpace(want) {
			return false
		}
	}
	return true
}

func isCoordinatorInvokeSession(sess *api.Session) bool {
	if sess == nil || strings.TrimSpace(sess.ParentSessionID) != "" {
		return false
	}
	agent := strings.TrimSpace(sess.AgentType)
	if agent == "" {
		agent = "coordinator"
	}
	return agent == "coordinator"
}
