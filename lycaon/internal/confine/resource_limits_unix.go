//go:build darwin || linux

package confine

// declaredResourceLimits: macOS and Linux query each resource and start a
// launched program with a raised or lowered soft limit or a lowered hard one.
func declaredResourceLimits() map[string]LimitCapability {
	full := LimitQuery | LimitSetSoft | LimitSetHard
	return map[string]LimitCapability{"core": full, "fsize": full, "nofile": full, "stack": full, "cpu": full, "nproc": full, "as": full}
}
