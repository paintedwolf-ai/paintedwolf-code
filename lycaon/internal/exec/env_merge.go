package exec

// MergeInlineEnv overlays per-call env atop session env; per-call keys win.
func MergeInlineEnv(session, perCall map[string]string) map[string]string {
	if len(session) == 0 && len(perCall) == 0 {
		return nil
	}
	if len(session) == 0 {
		return mapsClone(perCall)
	}
	if len(perCall) == 0 {
		return mapsClone(session)
	}
	out := mapsClone(session)
	for k, v := range perCall {
		out[k] = v
	}
	return out
}

func mapsClone(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
