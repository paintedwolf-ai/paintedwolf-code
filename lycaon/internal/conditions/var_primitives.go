package conditions

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
