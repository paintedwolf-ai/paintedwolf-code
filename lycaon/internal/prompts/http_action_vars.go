package prompts

// The HTTP-action teaching group: http_request, with fetch_url and wait as the
// alternatives its copy points at. Teaching renders for a tool on this turn's
// schema; one that is not offered stays untaught until it loads.
var httpActionTools = []string{"http_request", "fetch_url", "wait"}

// MergeHTTPActionVars publishes availability for the HTTP-action group from
// this turn's offered schemas.
func MergeHTTPActionVars(offered []string, into map[string]any) {
	if into == nil {
		return
	}
	set := toolNameSet(offered)
	for _, tool := range httpActionTools {
		into["profile_has_"+tool] = set[tool]
	}
}

// MergeWebResearchUnavailable withdraws fetch_url from the teaching when the
// session has web research disabled: the roster still lists the tool, but the
// host refuses the call.
func MergeWebResearchUnavailable(into map[string]any) {
	if into == nil {
		return
	}
	into["profile_has_fetch_url"] = false
}
