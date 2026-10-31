package curl

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
)

// colonFields splits curl's HOST:PORT:... syntax; a bracketed IPv6 host
// keeps its colons.
func colonFields(value string) []string {
	var fields []string
	bracketed, start := false, 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '[':
			bracketed = true
		case ']':
			bracketed = false
		case ':':
			if !bracketed {
				fields = append(fields, value[start:i])
				start = i + 1
			}
		}
	}
	return append(fields, value[start:])
}

func unbracket(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// port parses a mapping port; empty is zero (any, or unchanged) when allowed.
func port(value string, optional bool) (uint16, bool) {
	if value == "" {
		return 0, optional
	}
	n, err := strconv.ParseUint(value, 10, 16)
	return uint16(n), err == nil && n > 0
}

// parseResolve reads --resolve [+]HOST:PORT:ADDR. The address may be a
// bracketed or bare IPv6 literal.
func parseResolve(value string) (outboundhttp.ResolveMapping, error) {
	invalid := usage(value, "HOST:PORT:ADDRESS with one IP address")
	fields := colonFields(strings.TrimPrefix(value, "+"))
	if len(fields) < 3 || fields[0] == "" || strings.HasPrefix(value, "-") {
		return outboundhttp.ResolveMapping{}, invalid
	}
	p, ok := port(fields[1], false)
	address, err := netip.ParseAddr(unbracket(strings.Join(fields[2:], ":")))
	if !ok || err != nil || address.Zone() != "" {
		return outboundhttp.ResolveMapping{}, invalid
	}
	return outboundhttp.ResolveMapping{Host: unbracket(fields[0]), Port: p, Address: address}, nil
}

// parseConnectTo reads --connect-to HOST1:PORT1:HOST2:PORT2. Empty HOST1 or
// PORT1 matches any; empty HOST2 or PORT2 keeps the URL's.
func parseConnectTo(value string) (outboundhttp.ResolveMapping, error) {
	invalid := usage(value, "HOST1:PORT1:HOST2:PORT2, where empty parts match any or keep the URL's")
	fields := colonFields(value)
	if len(fields) != 4 {
		return outboundhttp.ResolveMapping{}, invalid
	}
	from, fromOK := port(fields[1], true)
	to, toOK := port(fields[3], true)
	if !fromOK || !toOK {
		return outboundhttp.ResolveMapping{}, invalid
	}
	mapping := outboundhttp.ResolveMapping{Host: unbracket(fields[0]), Port: from, TargetPort: to}
	target := unbracket(fields[2])
	if address, err := netip.ParseAddr(target); err == nil && address.Zone() == "" {
		mapping.Address = address
	} else {
		mapping.TargetHost = target
	}
	return mapping, nil
}

// composeDial orders --connect-to before --resolve and, as curl does,
// resolves a connect-to host through a --resolve entry for it.
func composeDial(connectTo, resolve []outboundhttp.ResolveMapping) []outboundhttp.ResolveMapping {
	for i, m := range connectTo {
		host, p := m.TargetHost, m.TargetPort
		if host == "" {
			host = m.Host
		}
		if p == 0 {
			p = m.Port
		}
		if m.Address.IsValid() || host == "" || p == 0 {
			continue
		}
		if dial, ok := outboundhttp.MappedDial(resolve, host, p); ok && dial.Address.IsValid() {
			connectTo[i].Address, connectTo[i].TargetPort = dial.Address, dial.Port
		}
	}
	return append(connectTo, resolve...)
}

// splitCertPassword splits --cert FILE[:PASSWORD]; "\:" is a literal colon
// and a Windows drive letter's colon is part of the file name.
func splitCertPassword(value string) (file, password string) {
	var b strings.Builder
	drive := len(value) > 2 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') &&
		(value[0] >= 'a' && value[0] <= 'z' || value[0] >= 'A' && value[0] <= 'Z')
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '\\' && i+1 < len(value) && value[i+1] == ':':
			b.WriteByte(':')
			i++
		case value[i] == ':' && !(drive && i == 1):
			return b.String(), value[i+1:]
		default:
			b.WriteByte(value[i])
		}
	}
	return b.String(), ""
}

// validRange accepts curl's -r forms: N-M, N-, -N, comma-separated.
func validRange(value string) bool {
	if value == "" {
		return false
	}
	for _, part := range strings.Split(value, ",") {
		first, last, ok := strings.Cut(part, "-")
		if !ok || first == "" && last == "" || !digits(first) || !digits(last) {
			return false
		}
	}
	return true
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
