package oar

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
)

func TestRequireKnownCatalogAnchor(t *testing.T) {
	ensureCatalog(t)
	if err := RequireKnownCatalogAnchor("tool.post_invoke"); err != nil {
		t.Fatalf("known: %v", err)
	}
	err := RequireKnownCatalogAnchor("tool.post")
	if err == nil || !strings.Contains(err.Error(), "unknown anchor") {
		t.Fatalf("want reject unknown tool.post anchor, got %v", err)
	}
	err = RequireKnownCatalogAnchor("not.a.real.anchor")
	if err == nil || !strings.Contains(err.Error(), "unknown anchor") {
		t.Fatalf("want reject unknown, got %v", err)
	}
}

func TestRequireKnownCatalogAnchorRequiresInstall(t *testing.T) {
	anchorcatalog.Clear()
	t.Cleanup(func() { ensureCatalog(t) })
	err := RequireKnownCatalogAnchor("tool.post_invoke")
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("want not installed, got %v", err)
	}
}

func TestToolPostAnchorUsesCatalogID(t *testing.T) {
	if AnchorToolPost != "tool.post_invoke" {
		t.Fatalf("AnchorToolPost=%q", AnchorToolPost)
	}
}
