package boot

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/workflow"
)

// ServeWiring is the production Prompt/gate configuration validated at serve start.
type ServeWiring struct {
	PostureRegistry *profiles.PostureRegistry
	BundledRules    map[string]*rules.RulesConfig
	RuleEngine      *rules.PostureRuleEngine
	SessionManager  *session.Host
	WorkflowManager *workflow.RunManager
}

// ValidateServeWiring fails closed when bundled config is not wired into runtime paths.
func ValidateServeWiring(w ServeWiring) error {
	if w.PostureRegistry == nil {
		return fmt.Errorf("posture registry required")
	}
	if len(w.BundledRules) == 0 {
		return fmt.Errorf("bundled rules required")
	}
	if err := rules.ValidatePostureRules(w.PostureRegistry, sessionposture.AllSessionPostures(), w.BundledRules); err != nil {
		return fmt.Errorf("posture rules: %w", err)
	}
	if w.RuleEngine == nil {
		return fmt.Errorf("PostureRuleEngine required for serve")
	}
	if w.RuleEngine.Postures == nil {
		return fmt.Errorf("PostureRuleEngine posture source required")
	}
	if w.SessionManager == nil {
		return fmt.Errorf("session manager required")
	}
	if w.WorkflowManager == nil {
		return fmt.Errorf("workflow manager required")
	}
	if w.WorkflowManager.Policy == nil || w.WorkflowManager.Requests == nil || w.WorkflowManager.Phases == nil || w.WorkflowManager.Store == nil {
		return fmt.Errorf("workflow runtime domains required")
	}
	for _, posture := range sessionposture.AllSessionPostures() {
		paths, err := w.PostureRegistry.RulesPaths(posture)
		if err != nil {
			return fmt.Errorf("posture %q rules: %w", posture, err)
		}
		for _, p := range paths {
			key := rules.NormalizeRulesPath(p)
			if _, ok := w.BundledRules[key]; !ok {
				return fmt.Errorf("posture %q: rule file %q not loaded", posture, key)
			}
		}
	}
	return nil
}
