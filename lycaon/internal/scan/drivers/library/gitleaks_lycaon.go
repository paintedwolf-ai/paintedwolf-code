package library

import (
	"regexp"

	gcfg "github.com/zricethezav/gitleaks/v8/config"
	glre "github.com/zricethezav/gitleaks/v8/regexp"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// overlayPathPat matches the shared project overlay directory.
func overlayPathPat() *glre.Regexp {
	return glre.MustCompile(`(?:^|[\\/])` + regexp.QuoteMeta(settingsoverlay.DirName()) + `(?:[\\/]|$)`)
}

func applyGitleaksLycaonExclude(cfg *gcfg.Config) error {
	allow := &gcfg.Allowlist{
		Description: "exclude engine sandboxes and metadata under the project overlay",
		Paths:       []*glre.Regexp{overlayPathPat()},
	}
	if err := allow.Validate(); err != nil {
		return err
	}
	cfg.Allowlists = append(cfg.Allowlists, allow)
	return nil
}
