package conditions

import (
	"strings"
)

// IsHostOnlyCondition reports ids that may only be written by host subsystems, not rule when: keys.
func IsHostOnlyCondition(name string) bool {
	name = strings.TrimSpace(name)
	return strings.HasPrefix(name, "var_set:")
}

func registerCoreVars(reg *ConditionRegistry) error {
	if reg == nil {
		return nil
	}
	if err := reg.RegisterParameterized("var_equals:", func(ec EvalContext) (bool, error) {
		path, value, ok := ParseVarEquals(ec.ConditionID)
		if !ok {
			return false, nil
		}
		return DotPathEquals(ec.Vars, path, value), nil
	}); err != nil {
		return err
	}
	return reg.RegisterParameterized("var_truthy:", func(ec EvalContext) (bool, error) {
		path, ok := ParseVarTruthy(ec.ConditionID)
		if !ok {
			return false, nil
		}
		return DotPathTruthy(ec.Vars, path), nil
	})
}
