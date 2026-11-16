package rules

import (
	"fmt"
	"sort"
	"strings"
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

// projectWhenMapKeys projects exact conditions into map keys.
func projectWhenMapKeys(reg *conditions.ConditionRegistry) []string {
	set := map[string]struct{}{}
	postureFamily := false
	for _, name := range reg.ExactNames() {
		if conditions.IsHostOnlyCondition(name) {
			continue
		}
		if strings.HasPrefix(name, "posture_is_") {
			// The map form carries posture as its value.
			postureFamily = true
			continue
		}
		set[name] = struct{}{}
	}
	for _, prefix := range reg.ParameterizedPrefixes() {
		if prefix == "posture_is:" {
			postureFamily = true
		}
	}
	if postureFamily {
		set["posture_is"] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
