package contract

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/usernotice"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func loadUserNoticeConfig(t *testing.T) *usernotice.Config {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	cfg, err := usernotice.LoadNoticeDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	contractcheck.FailErr(t, "load host/user-notices", err)
	return cfg
}

func noticeSurfaceCodes(cfg *usernotice.Config, surface string) []string {
	var codes []string
	for code, entry := range cfg.UserNotices {
		if entry.IsUserVisible() && entry.HasSurface(surface) {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}
