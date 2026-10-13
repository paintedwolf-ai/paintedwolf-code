package systemproxy

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

type desktopMode string

const (
	modeNone   desktopMode = "none"
	modeManual desktopMode = "manual"
	modeAuto   desktopMode = "auto"
)

// desktopConfiguration is one desktop's declared proxy policy.
type desktopConfiguration struct {
	Source Source
	Mode   desktopMode
	HTTP   *url.URL
	HTTPS  *url.URL
	SOCKS  *url.URL
	// SameProxy applies the HTTP proxy to every scheme without its own entry.
	SameProxy bool
	Bypass    []string
	// BypassInverted proxies only the listed hosts.
	BypassInverted bool
	// Script is the auto-configuration location; empty in auto mode means discovery.
	Script string
}

func (c desktopConfiguration) proxyFor(target *url.URL) (*url.URL, error) {
	switch c.Mode {
	case modeAuto:
		mechanism := "proxy auto-configuration"
		if c.Script == "" {
			mechanism = "proxy auto-discovery"
		}
		return nil, &Unsupported{Source: c.Source, Mechanism: mechanism, Reference: c.Script}
	case modeManual:
	default:
		return nil, nil
	}
	if matchesBypass(c.Bypass, target) != c.BypassInverted {
		return nil, nil
	}
	var proxy *url.URL
	switch strings.ToLower(target.Scheme) {
	case "https":
		proxy = c.HTTPS
		if proxy == nil && c.SameProxy {
			proxy = c.HTTP
		}
	case "http":
		proxy = c.HTTP
	}
	if proxy == nil {
		proxy = c.SOCKS
	}
	return proxy, nil
}

// matchesBypass applies desktop exception lists: hostnames, domain suffixes
// written with a leading dot or wildcard, address literals and CIDR ranges.
// Names are never resolved here.
func matchesBypass(entries []string, target *url.URL) bool {
	host := strings.ToLower(strings.TrimSuffix(strings.Trim(target.Hostname(), "[]"), "."))
	if host == "" {
		return false
	}
	address := net.ParseIP(host)
	for _, entry := range entries {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err == nil && address != nil && network.Contains(address) {
				return true
			}
			continue
		}
		if ip := net.ParseIP(strings.Trim(entry, "[]")); ip != nil {
			if address != nil && ip.Equal(address) {
				return true
			}
			continue
		}
		domain := strings.TrimPrefix(strings.TrimPrefix(entry, "*"), ".")
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// manualProxy builds one proxy endpoint; an empty host or port means none.
func manualProxy(scheme, host string, port int, user *url.Userinfo) *url.URL {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 || port > 65535 {
		return nil
	}
	return &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port)), User: user}
}
