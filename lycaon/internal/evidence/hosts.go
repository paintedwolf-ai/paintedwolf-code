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

// ContactsFromLedger returns how this chat reached each host. Hosts that only
// appear in ingested content, such as search results or a fetched page, are
// not contacts, and the chat's own dial outranks a retrieval.
func ContactsFromLedger(ev Ledger) map[string]Contact {
	out := make(map[string]Contact)
	for handle, record := range ev.Handles {
		if record.SourceTool != DialedHostSourceTool {
			continue
		}
		contact := ContactNone
		switch {
		case strings.HasPrefix(handle, DialedHostHandlePrefix):
			contact = ContactOwn
		case strings.HasPrefix(handle, AuditedHostHandlePrefix):
			contact = ContactAudited
		case strings.HasPrefix(handle, RetrievedHostHandlePrefix):
			contact = ContactRetrieval
		}
		if h := HostKey(record.URL); h != "" && contact > out[h] {
			out[h] = contact
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
