package browser

import (
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/confine"
)

// browserBoundary resolves confinement and rejects missing enforced boundaries.
func browserBoundary(roots []string) (*confine.Confinement, bool, error) {
	conf, confined := confine.BrowserConfinement(confine.Request{Roots: roots})
	if err := confine.RequireApplied(confined); err != nil {
		data := map[string]any{
			"reason": "confinement_unavailable",
			"detail": err.Error(),
		}
		if caps, linux := confine.LinuxCapabilities(); linux {
			data["linux_capabilities"] = caps
		}
		return nil, false, browserengine.Reject("BROWSER_UNAVAILABLE", data)
	}
	return conf, confined, nil
}
