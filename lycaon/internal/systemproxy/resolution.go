// Package systemproxy resolves the proxy the operating system configures for
// host networking. Explicit environment variables are the caller's layer above
// this package; nothing here falls back to a direct connection when a declared
// mechanism cannot be honoured.
package systemproxy

import (
	"fmt"
	"net/url"
	"strings"
)

// Source names the configuration a resolution came from.
type Source string

const (
	// SourceNone means no desktop proxy configuration applies.
	SourceNone Source = "none"
	// SourceMacOS is the CFNetwork system proxy configuration.
	SourceMacOS Source = "macos"
	// SourceGSettings is the GNOME desktop configuration schema org.gnome.system.proxy.
	SourceGSettings Source = "gsettings"
	// SourceKIO is the KDE proxy configuration file kioslaverc.
	SourceKIO Source = "kioslaverc"
)

// Unsupported reports a declared proxy mechanism the host cannot execute.
// It is returned instead of a direct connection so declared policy is never
// silently bypassed.
type Unsupported struct {
	Source    Source
	Mechanism string
	// Reference is the configured script location, when the mechanism has one.
	Reference string
}

func (u *Unsupported) Error() string {
	message := fmt.Sprintf("system proxy: %s configures %s, which Painted Wolf Code cannot run on this platform", u.Source, u.Mechanism)
	if u.Reference != "" {
		message += " (" + u.Reference + ")"
	}
	return message + "; " + u.Guidance()
}

// Guidance names the explicit configuration path that replaces the mechanism.
func (*Unsupported) Guidance() string {
	return "set HTTPS_PROXY, HTTP_PROXY and NO_PROXY in the environment that launches the app, or switch the desktop proxy to a manual host and port"
}

// Redact renders a proxy for diagnostics with credentials removed.
func Redact(proxy *url.URL) string {
	if proxy == nil {
		return "direct"
	}
	copied := *proxy
	if copied.User != nil {
		if _, hasPassword := copied.User.Password(); hasPassword || copied.User.Username() != "" {
			copied.User = url.User("redacted")
		}
	}
	return strings.TrimSuffix(copied.String(), "/")
}
