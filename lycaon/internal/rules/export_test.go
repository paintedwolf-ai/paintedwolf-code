package rules

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
)

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
