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

// ContactedHostsFromLedger returns the hosts this chat's own outbound dials
// reached. Hosts that only appear in ingested content, such as search results
// or a fetched page, are not contacts.
func ContactedHostsFromLedger(ev Ledger) map[string]struct{} {
	out := make(map[string]struct{})
	for handle, record := range ev.Handles {
		if !strings.HasPrefix(handle, DialedHostHandlePrefix) || record.SourceTool != DialedHostSourceTool {
			continue
		}
		if h := HostKey(record.URL); h != "" {
			out[h] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
