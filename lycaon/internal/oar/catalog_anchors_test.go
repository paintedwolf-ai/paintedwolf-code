package oar

import (
	"testing"
)

func TestToolPostAnchorUsesCatalogID(t *testing.T) {
	if AnchorToolPost != "tool.post_invoke" {
		t.Fatalf("AnchorToolPost=%q", AnchorToolPost)
	}
}
