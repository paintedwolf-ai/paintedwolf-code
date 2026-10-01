package usernotice

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExtensionSourceFailuresHaveActionableBoundedNotices(t *testing.T) {
	cfg, err := LoadNoticeDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load extension notices", err)
	catalog := NewCatalog(cfg)
	for _, code := range []string{
		"choice_source_invalid", "choice_source_failed", "choice_source_not_found",
		"contribution_search_invalid", "contribution_search_failed", "search_source_not_found",
		"contribution_source_unavailable", "operation_output_invalid",
	} {
		t.Run(code, func(t *testing.T) {
			entry, ok := cfg.UserNotices[code]
			if !ok || !entry.HasSurface("http") || (entry.UseDefaults != nil && *entry.UseDefaults) {
				t.Fatal("extension failure lacks its own HTTP notice")
			}
			const providerPayload = "untrusted-provider-private-body"
			copy := catalog.RenderWire(code, map[string]any{"reason": providerPayload})
			if copy.Title == "" || copy.Message == "" || copy.SuggestedAction == "" || copy.Message == cfg.Defaults.Message {
				t.Fatalf("extension failure lost actionable context: %+v", copy)
			}
			if strings.Contains(copy.Message+copy.SuggestedAction, providerPayload) {
				t.Fatal("provider output leaked into an unbounded diagnostic")
			}
			if catalog.Retryable(code) {
				t.Fatal("extension failure should require an explicit reviewed retry")
			}
		})
	}
}
