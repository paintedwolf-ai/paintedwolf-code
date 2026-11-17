package contract

import (
	"path/filepath"
	"strings"
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

func TestSessionAbortedDocumentedNotUserVisible(t *testing.T) {
	cfg := loadUserNoticeConfig(t)
	entry, ok := cfg.UserNotices["session_aborted"]
	if !ok {
		t.Fatal("host/user-notices must document session_aborted with user_visible:false")
	}
	if entry.IsUserVisible() {
		t.Fatal("session_aborted must be user_visible:false")
	}
}

func TestAppBuildWiresUserNoticeCatalog(t *testing.T) {
	text := contractcheck.ServeWireSource(t)
	if !strings.Contains(text, "deps.Core.UserNotices = ") {
		t.Fatal("serve build graph must hand the user notice catalog to the HTTP server's dependencies")
	}
	if !strings.Contains(text, "LoadEffectiveUserNotices") {
		t.Fatal("serve build graph must load the user notice catalog via LoadEffectiveUserNotices")
	}
}
