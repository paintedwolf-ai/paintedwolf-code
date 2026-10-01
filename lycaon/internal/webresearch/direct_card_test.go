package webresearch

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestDirectCardContent(t *testing.T) {
	content := DirectCardContent()
	if content.ProviderID != string(wire.WebSearchProviderDirect) {
		t.Fatalf("provider_id = %q", content.ProviderID)
	}
	if content.Kind != "direct" {
		t.Fatalf("kind = %q", content.Kind)
	}
	if content.Label == "" {
		t.Fatalf("content = %+v", content)
	}
}
