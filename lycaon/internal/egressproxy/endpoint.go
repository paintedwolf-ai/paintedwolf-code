package egressproxy

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Transport identifies which broker front door observed a destination.
type Transport string

const (
	TransportHTTPConnect Transport = "http_connect"
	TransportHTTPRequest Transport = "http_request"
	TransportSocksTCP    Transport = "socks_tcp"
)

// Opaque reports whether this transport hands the broker an unreadable byte
// stream. For an opaque transport the destination port is the protocol being
// authorized, so every surface that names one — card copy, grant identity,
// verdict cache — reads this single answer.
func (t Transport) Opaque() bool {
	switch t {
	case TransportSocksTCP, TransportHTTPConnect:
		return true
	default:
		return false
	}
}

// Endpoint is a broker-internal destination identity.
type Endpoint struct {
	// Inherited marks a destination reached by a process an earlier command left
	// running, which this action now answers for. The action did not make the
	// request, so its receipt must not read as though it did.
	Inherited     bool
	Transport     Transport
	Host          string // normalized, no brackets for IPv6
	Port          uint16
	RequestMethod string // plain HTTP only; empty for CONNECT and SOCKS
	RequestPath   string // escaped path without query; plain HTTP only
}

// ParseHTTPEndpoint parses host or host:port for an HTTP front door.
// Port defaults to 443 for http_connect and 80 for http_request when absent.
func ParseHTTPEndpoint(hostport string, transport Transport) (Endpoint, error) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return Endpoint{}, fmt.Errorf("empty host")
	}
	def := uint16(443)
	if transport == TransportHTTPRequest {
		def = 80
	}
	ep, err := normalizeHostPort(hostport, def)
	if err != nil {
		return Endpoint{}, err
	}
	ep.Transport = transport
	return ep, nil
}

// ParseSocksEndpoint builds an endpoint from a SOCKS CONNECT destination.
func ParseSocksEndpoint(atyp byte, addr []byte, port uint16) (Endpoint, error) {
	if port == 0 {
		return Endpoint{}, fmt.Errorf("zero port")
	}
	host, err := parseSocksAddress(atyp, addr)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		Transport: TransportSocksTCP,
		Host:      host,
		Port:      port,
	}, nil
}

// String returns a stable debug form without secrets.
func (e Endpoint) String() string {
	if e.Port == 0 {
		return string(e.Transport) + "://" + e.Host
	}
	return string(e.Transport) + "://" + net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port)))
}

// DialAddr returns host:port for net.Dial.
func (e Endpoint) DialAddr() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port)))
}

func normalizeHostPort(hostport string, defaultPort uint16) (Endpoint, error) {
	if strings.ContainsAny(hostport, "\x00\r\n\t") || !utf8.ValidString(hostport) {
		return Endpoint{}, fmt.Errorf("invalid host")
	}
	host := hostport
	port := defaultPort
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host = h
		if strings.HasPrefix(hostport, "[") {
			addr, parseErr := netip.ParseAddr(host)
			if parseErr != nil || !addr.Is6() {
				return Endpoint{}, fmt.Errorf("malformed hostport")
			}
		}
		n, err := parsePort(p)
		if err != nil {
			return Endpoint{}, err
		}
		port = n
	} else if strings.HasPrefix(hostport, "[") {
		if !strings.HasSuffix(hostport, "]") {
			return Endpoint{}, fmt.Errorf("malformed hostport")
		}
		host = hostport
	}
	host, err := normalizeHost(host)
	if err != nil {
		return Endpoint{}, err
	}
	if port == 0 {
		return Endpoint{}, fmt.Errorf("zero port")
	}
	return Endpoint{Host: host, Port: port}, nil
}

func normalizeHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("empty host")
	}
	if strings.ContainsAny(host, "\x00\r\n\t") || !utf8.ValidString(host) {
		return "", fmt.Errorf("invalid host")
	}
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
			return "", fmt.Errorf("invalid host")
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		addr, err := netip.ParseAddr(inner)
		if err != nil || !addr.Is6() {
			return "", fmt.Errorf("invalid host")
		}
		return addr.String(), nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String(), nil
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", fmt.Errorf("empty host")
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("invalid host")
	}
	ascii = strings.ToLower(ascii)
	if ascii == "" || strings.Contains(ascii, "/") {
		return "", fmt.Errorf("invalid host")
	}
	return ascii, nil
}

func parsePort(raw string) (uint16, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty port")
	}
	n, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port")
	}
	return uint16(n), nil
}

func parseSocksAddress(atyp byte, addr []byte) (string, error) {
	switch atyp {
	case 0x01:
		if len(addr) != 4 {
			return "", fmt.Errorf("invalid ipv4 address")
		}
		return netip.AddrFrom4([4]byte(addr)).String(), nil
	case 0x04:
		if len(addr) != 16 {
			return "", fmt.Errorf("invalid ipv6 address")
		}
		return netip.AddrFrom16([16]byte(addr)).String(), nil
	case 0x03:
		if len(addr) == 0 || len(addr) > 255 {
			return "", fmt.Errorf("invalid domain length")
		}
		domain := string(addr)
		if !utf8.ValidString(domain) || strings.ContainsAny(domain, "\x00\r\n\t") {
			return "", fmt.Errorf("invalid domain")
		}
		return normalizeHost(domain)
	default:
		return "", fmt.Errorf("unsupported address type")
	}
}
