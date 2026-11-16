package rules

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/conditions"
)

var (
	whenKeysOnce sync.Once
	whenKeys     []string
	whenKeysErr  error
)

// RuleWhenKeys returns the fixed posture-rule map keys.
func RuleWhenKeys() []string {
	whenKeysOnce.Do(func() {
		reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
		if err != nil {
			whenKeysErr = err
			return
		}
		if err := RegisterRuleConditions(reg); err != nil {
			whenKeysErr = err
			return
		}
		whenKeys = projectWhenMapKeys(reg)
	})
	if whenKeysErr != nil {
		panic(fmt.Sprintf("rules: RuleWhenKeys: %v", whenKeysErr))
	}
	out := make([]string, len(whenKeys))
	copy(out, whenKeys)
	return out
}
