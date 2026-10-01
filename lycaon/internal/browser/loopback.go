package browser

import (
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
)

// IsLoopbackURL reports whether raw is http(s) with a loopback host (127.0.0.1, ::1, localhost).
func IsLoopbackURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	return egress.SyntacticLoopback(u.Hostname())
}
