package evidence

import (
	"net/url"
	"strings"
)

// DialedHostHandlePrefix keys mediated egress observations.
const DialedHostHandlePrefix = "dialed_host#"

// DialedHostSourceTool identifies the mediated egress plane.
const DialedHostSourceTool = "network_egress"

// DialedHostRecord creates one mediated egress observation.
func DialedHostRecord(host string) (Record, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.ContainsAny(host, "/ \t") {
		return Record{}, false
	}
	return Record{
		Handle:     DialedHostHandlePrefix + host,
		Kind:       "network",
		Shape:      ShapeURL,
		Fidelity:   FidelityStructured,
		SourceTool: DialedHostSourceTool,
		URL:        (&url.URL{Scheme: "https", Host: host}).String(),
	}, true
}

// HostVisited reports whether host is already in a visited-host set.
func HostVisited(visited map[string]struct{}, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || len(visited) == 0 {
		return false
	}
	_, ok := visited[host]
	return ok
}
