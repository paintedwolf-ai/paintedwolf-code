package systemproxy

import (
	"net"
	"strconv"
	"strings"
)

// ExceptionKind classifies one proxy exception entry.
type ExceptionKind uint8

const (
	// ExceptionAll exempts every destination.
	ExceptionAll ExceptionKind = iota + 1
	// ExceptionDomain exempts a name and every name beneath it.
	ExceptionDomain
	// ExceptionSubdomains exempts the names beneath Domain but not Domain itself.
	ExceptionSubdomains
	// ExceptionAddress exempts one address literal.
	ExceptionAddress
	// ExceptionNetwork exempts every address in a CIDR range.
	ExceptionNetwork
	// ExceptionGlob exempts names matching a pattern of `*` and `?` wildcards.
	ExceptionGlob
	// ExceptionSimpleHostnames exempts names without a dot.
	ExceptionSimpleHostnames
	// ExceptionInvalid is an entry the host cannot read; it exempts nothing.
	ExceptionInvalid
)

// Exception is one entry of a proxy exception list.
type Exception struct {
	Kind ExceptionKind
	// Domain is the name for ExceptionDomain and ExceptionSubdomains, and the
	// pattern for ExceptionGlob.
	Domain  string
	Address net.IP
	Network *net.IPNet
	// Port restricts the entry to one destination port; empty means any.
	Port string
	// Raw is the entry as configured.
	Raw string
}

// Matches reports whether the entry exempts host on port. Names are never resolved.
func (e Exception) Matches(host, port string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" || (e.Port != "" && e.Port != port) {
		return false
	}
	address := net.ParseIP(host)
	switch e.Kind {
	case ExceptionAll:
		return true
	case ExceptionDomain:
		return address == nil && (host == e.Domain || strings.HasSuffix(host, "."+e.Domain))
	case ExceptionSubdomains:
		return address == nil && strings.HasSuffix(host, "."+e.Domain)
	case ExceptionAddress:
		return address != nil && address.Equal(e.Address)
	case ExceptionNetwork:
		return address != nil && e.Network.Contains(address)
	case ExceptionGlob:
		return globMatch(e.Domain, host)
	case ExceptionSimpleHostnames:
		return address == nil && !strings.Contains(host, ".")
	default:
		return false
	}
}

// ParseDesktopException reads one entry in the grammar desktop proxy settings
// share: `*`, a host name with an optional `*.` or `.` prefix (the name and
// its subdomains), an address literal, a CIDR range (IPv4 octets may be
// abbreviated as in `169.254/16`), or a pattern with `*` and `?` wildcards.
func ParseDesktopException(raw string) Exception {
	entry := strings.ToLower(strings.TrimSpace(raw))
	exception := Exception{Raw: raw, Kind: ExceptionInvalid}
	switch {
	case entry == "":
		return exception
	case entry == "*":
		exception.Kind = ExceptionAll
	case strings.Contains(entry, "/"):
		if network, ok := parseNetwork(entry); ok {
			exception.Kind, exception.Network = ExceptionNetwork, network
		}
	case net.ParseIP(strings.Trim(entry, "[]")) != nil:
		exception.Kind, exception.Address = ExceptionAddress, net.ParseIP(strings.Trim(entry, "[]"))
	default:
		domain := strings.TrimPrefix(strings.TrimPrefix(entry, "*."), ".")
		switch {
		case validHostName(domain):
			exception.Kind, exception.Domain = ExceptionDomain, domain
		case validHostPattern(entry):
			exception.Kind, exception.Domain = ExceptionGlob, entry
		}
	}
	return exception
}

// parseNetwork accepts CIDR notation and IPv4 prefixes with omitted trailing octets.
func parseNetwork(entry string) (*net.IPNet, bool) {
	if _, network, err := net.ParseCIDR(entry); err == nil {
		return network, true
	}
	prefix, bits, ok := strings.Cut(entry, "/")
	if !ok || strings.Contains(prefix, ":") {
		return nil, false
	}
	octets := strings.Split(prefix, ".")
	if len(octets) == 0 || len(octets) > 4 {
		return nil, false
	}
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	if size, err := strconv.Atoi(bits); err != nil || size < 0 || size > 32 {
		return nil, false
	}
	_, network, err := net.ParseCIDR(strings.Join(octets, ".") + "/" + bits)
	return network, err == nil
}

func validHostName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

func validHostPattern(pattern string) bool {
	if !strings.ContainsAny(pattern, "*?") {
		return false
	}
	return validHostName(strings.NewReplacer("*", "a", "?", "a").Replace(pattern))
}

// globMatch matches `*` (any run, including dots) and `?` (one character).
func globMatch(pattern, name string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			for i := len(name); i >= 0; i-- {
				if globMatch(pattern[1:], name[i:]) {
					return true
				}
			}
			return false
		case '?':
			if name == "" {
				return false
			}
		default:
			if name == "" || name[0] != pattern[0] {
				return false
			}
		}
		pattern, name = pattern[1:], name[1:]
	}
	return name == ""
}
