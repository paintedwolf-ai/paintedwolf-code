package confine

import (
	"crypto/sha256"
	"encoding/base64"
	"net"
	"sort"
	"strconv"
	"strings"
)

// MaxDirectIPPermits caps transport narrowing per action.
const MaxDirectIPPermits = 16

// DirectIPPermit allows one protocol and port without restricting the remote host.
type DirectIPPermit struct {
	// Protocol is tcp or udp.
	Protocol string
	Port     uint16
}

func (p DirectIPPermit) String() string {
	return p.Protocol + "/" + strconv.Itoa(int(p.Port))
}

// ParseDirectIPPermits derives all-or-nothing transport narrowing.
func ParseDirectIPPermits(declared []string) ([]DirectIPPermit, bool) {
	if len(declared) == 0 {
		return nil, false
	}
	seen := map[DirectIPPermit]struct{}{}
	var permits []DirectIPPermit
	for _, raw := range declared {
		parsed, ok := parseOneDestination(raw)
		if !ok {
			return nil, false
		}
		for _, p := range parsed {
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			permits = append(permits, p)
		}
	}
	if len(permits) == 0 || len(permits) > MaxDirectIPPermits {
		return nil, false
	}
	sort.Slice(permits, func(i, j int) bool {
		if permits[i].Port != permits[j].Port {
			return permits[i].Port < permits[j].Port
		}
		return permits[i].Protocol < permits[j].Protocol
	})
	return permits, true
}

func parseOneDestination(raw string) ([]DirectIPPermit, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, false
	}
	protocols := []string{"tcp", "udp"}
	if scheme, rest, found := strings.Cut(s, "://"); found {
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "tcp":
			protocols = []string{"tcp"}
		case "udp":
			protocols = []string{"udp"}
		default:
			// Unknown schemes have no authoritative port mapping.
			return nil, false
		}
		s = rest
	}
	host, portText, err := net.SplitHostPort(s)
	if err != nil || strings.TrimSpace(host) == "" {
		return nil, false
	}
	port, err := strconv.ParseUint(strings.TrimSpace(portText), 10, 16)
	if err != nil || port == 0 {
		return nil, false
	}
	out := make([]DirectIPPermit, 0, len(protocols))
	for _, proto := range protocols {
		out = append(out, DirectIPPermit{Protocol: proto, Port: uint16(port)})
	}
	return out, true
}

// DirectIPPermitsDigest identifies an applied permit set.
func DirectIPPermitsDigest(permits []DirectIPPermit) string {
	if len(permits) == 0 {
		return ""
	}
	parts := make([]string, 0, len(permits))
	for _, p := range permits {
		parts = append(parts, p.String())
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
