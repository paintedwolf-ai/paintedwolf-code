package contract

import (
	"path/filepath"
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
