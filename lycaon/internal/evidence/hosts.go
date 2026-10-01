package evidence

import (
	"net/url"
	"strings"
)

// HostKey normalizes a URL to a lowercase hostname for visited-host membership.
// Empty when the URL has no usable host.
func HostKey(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(u.Hostname()))
}

// VisitedHostsFromLedger returns the set of hosts observed on ledger URL records
// (web_search harvest, fetch_url targets, and worker-merged URLs).
func VisitedHostsFromLedger(ev Ledger) map[string]struct{} {
	out := make(map[string]struct{})
	for _, raw := range ObservedURLsSorted(ev) {
		if h := HostKey(raw); h != "" {
			out[h] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
