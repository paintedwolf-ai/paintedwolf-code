package surface

import "strings"

var watchManagementAllowlist = map[string]struct{}{
	"implement_investigate":     {},
	"observe_investigate":       {},
	"implement_routing":         {},
	"plan_research":             {},
	"plan_review":               {},
	"survey_execute":            {},
	"review_adjudicate":         {},
	"decision_adjudicate":       {},
	"plan_execute":              {},
	"implement_park":            {},
	"implement_dispatch":        {},
	"implement_overlay_promote": {},
}

// AllowsWatchManagement reports whether surfaceID admits watch resource controls.
func AllowsWatchManagement(surfaceID string) bool {
	_, ok := watchManagementAllowlist[strings.TrimSpace(surfaceID)]
	return ok
}
