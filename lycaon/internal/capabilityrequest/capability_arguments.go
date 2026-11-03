package capabilityrequest

func BoolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	v, ok := args[key].(bool)
	return ok && v
}
