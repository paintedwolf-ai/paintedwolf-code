package usernotice

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGitFailuresRetainOperationContext(t *testing.T) {
	cfg, err := LoadNoticeDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load Git notices", err)
	catalog := NewCatalog(cfg)
	count := 0
	for code, entry := range cfg.UserNotices {
		if !strings.HasPrefix(code, "git_") || !strings.HasSuffix(code, "_failed") {
			continue
		}
		count++
		t.Run(code, func(t *testing.T) {
			if !entry.HasSurface("http") || (entry.UseDefaults != nil && *entry.UseDefaults) {
				t.Fatal("Git operation lost its own HTTP recovery notice")
			}
			const privateDiagnostic = "remote-with-private-credentials"
			copy := catalog.RenderWire(code, map[string]any{"reason": privateDiagnostic})
			if copy.Title == "" || copy.Message == "" || copy.SuggestedAction == "" || copy.Message == cfg.Defaults.Message {
				t.Fatalf("Git failure lost operation and recovery context: %+v", copy)
			}
			if strings.Contains(copy.Message+copy.SuggestedAction, privateDiagnostic) {
				t.Fatal("untrusted Git diagnostic leaked into the notice")
			}
			if catalog.Retryable(code) {
				t.Fatal("Git mutations require an explicit reviewed retry")
			}
		})
	}
	if count == 0 {
		t.Fatal("Git failure catalog was not exercised")
	}
}
