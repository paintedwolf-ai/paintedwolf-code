package mcp

import (
	"errors"
	neturl "net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
)

var (
	errEmptyURL   = errors.New("empty url")
	errInvalidURL = errors.New("invalid url")
)

// ParsedHTTPURL is a catalog HTTP MCP address after scheme inference.
type ParsedHTTPURL struct {
	Canonical string
	Scheme    string
	Loopback  bool
}

// ParseHTTPURL returns the canonical HTTP MCP URL.
// Schemeless host:port is http on loopback and https otherwise.
func ParseHTTPURL(raw string) (ParsedHTTPURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ParsedHTTPURL{}, errEmptyURL
	}
	if strings.HasPrefix(raw, "/") {
		return ParsedHTTPURL{}, errInvalidURL
	}
	if strings.Contains(raw, "://") {
		return parseAbsoluteHTTPURL(raw)
	}
	return parseSchemelessHTTPURL(bracketBareIPv6(raw))
}

// ClassifyHTTPURL returns the persisted URL and a catalog reject code.
func ClassifyHTTPURL(raw string) (canonical string, loopback bool, reject string) {
	parsed, err := ParseHTTPURL(raw)
	if err != nil {
		return "", false, RejectInvalidURL
	}
	if !parsed.Loopback && parsed.Scheme != "https" {
		return parsed.Canonical, false, RejectRemoteRequiresHTTPS
	}
	return parsed.Canonical, parsed.Loopback, ""
}

func parseSchemelessHTTPURL(raw string) (ParsedHTTPURL, error) {
	// url.Parse("host:port") takes host as the scheme; prefix so Host parses.
	parsed, err := parseHTTPURLString("http://" + raw)
	if err != nil {
		return ParsedHTTPURL{}, err
	}
	if parsed.Loopback {
		return parsed, nil
	}
	u, err := neturl.Parse(parsed.Canonical)
	if err != nil {
		return ParsedHTTPURL{}, errInvalidURL
	}
	u.Scheme = "https"
	return ParsedHTTPURL{Canonical: u.String(), Scheme: "https", Loopback: false}, nil
}

func parseAbsoluteHTTPURL(raw string) (ParsedHTTPURL, error) {
	parsed, err := parseHTTPURLString(raw)
	if err != nil {
		return ParsedHTTPURL{}, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ParsedHTTPURL{}, errInvalidURL
	}
	return parsed, nil
}

func parseHTTPURLString(raw string) (ParsedHTTPURL, error) {
	u, err := neturl.Parse(raw)
	if err != nil || strings.TrimSpace(u.Host) == "" || u.User != nil {
		return ParsedHTTPURL{}, errInvalidURL
	}
	host := u.Hostname()
	if host == "" {
		return ParsedHTTPURL{}, errInvalidURL
	}
	return ParsedHTTPURL{
		Canonical: u.String(),
		Scheme:    u.Scheme,
		Loopback:  egress.SyntacticLoopback(host),
	}, nil
}

func bracketBareIPv6(raw string) string {
	switch {
	case raw == "::1":
		return "[::1]"
	case strings.HasPrefix(raw, "::1/") || strings.HasPrefix(raw, "::1?"):
		return "[::1]" + raw[3:]
	default:
		return raw
	}
}

func isLoopbackURL(raw string) bool {
	parsed, err := ParseHTTPURL(raw)
	return err == nil && parsed.Loopback
}
