package evidence

import (
	"net/url"
	"strings"
)

// DialedHostHandlePrefix keys mediated egress observations.
const DialedHostHandlePrefix = "dialed_host#"

// DialedHostSourceTool identifies the mediated egress plane.
const DialedHostSourceTool = "network_egress"

// RetrievedHostHandlePrefix keys hosts a retrieval capability reached.
const RetrievedHostHandlePrefix = "retrieved_host#"

// Contact is how this chat reached a host.
type Contact uint8

const (
	ContactNone Contact = iota
	// ContactRetrieval is a retrieval capability reaching one of its sources.
	ContactRetrieval
	// ContactOwn is this chat's own dial.
	ContactOwn
)

// DialedHostRecord records one allowed dial of the chat's own.
func DialedHostRecord(host string) (Record, bool) {
	return contactRecord(DialedHostHandlePrefix, host)
}

// RetrievedHostRecord records a retrieval capability reaching a source.
func RetrievedHostRecord(host string) (Record, bool) {
	return contactRecord(RetrievedHostHandlePrefix, host)
}

func contactRecord(prefix, host string) (Record, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.ContainsAny(host, "/ \t") {
		return Record{}, false
	}
	return Record{
		Handle:     prefix + host,
		Kind:       "network",
		Shape:      ShapeURL,
		Fidelity:   FidelityStructured,
		SourceTool: DialedHostSourceTool,
		URL:        (&url.URL{Scheme: "https", Host: host}).String(),
	}, true
}
