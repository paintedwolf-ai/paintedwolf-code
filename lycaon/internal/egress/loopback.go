package egress

import (
	"net/netip"
	"strings"
)

// loopbackName is reserved for the loopback interface by RFC 6761.
const loopbackName = "localhost"

// foldHostSpelling normalizes syntax that does not change host identity.
func foldHostSpelling(host string) string {
	host = strings.TrimSpace(host)
	if len(host) > 1 && strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

// LoopbackLiteral reports whether host is a loopback address literal.
func LoopbackLiteral(host string) bool {
	folded := foldHostSpelling(host)
	if folded == "" {
		return false
	}
	addr, err := netip.ParseAddr(folded)
	return err == nil && addr.Unmap().IsLoopback()
}

// SyntacticLoopback reports whether host explicitly names loopback.
func SyntacticLoopback(host string) bool {
	if foldHostSpelling(host) == loopbackName {
		return true
	}
	return LoopbackLiteral(host)
}
