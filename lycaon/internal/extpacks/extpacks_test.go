package extpacks

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoverStock(t *testing.T) {
	packs, err := DiscoverStock()
	testutil.FailErr(t, "DiscoverStock failed", err)
	want := map[string]bool{
		"painted-wolf/platform": true, "painted-wolf/security": true,
		"painted-wolf/plan": true, "painted-wolf/implement": true,
		"painted-wolf/options": true, "painted-wolf/bugbash": true,
		"painted-wolf/recon-pack":      true,
		"painted-wolf/security-survey": true, "painted-wolf/web-research": true,
		"painted-wolf/scan-guidance": true, "painted-wolf/browser": true,
		"painted-wolf/hitl": true, "painted-wolf/themes": true,
	}
	if len(packs) != len(want) {
		t.Fatalf("got %d packs, want %d", len(packs), len(want))
	}
	for _, p := range packs {
		if !want[p.ID] {
			t.Fatalf("unexpected pack %q", p.ID)
		}
	}
}

func TestUniqueUnitIDsAcrossPolicy(t *testing.T) {
	content, err := DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent failed", err)
	seen := map[string]string{}
	for _, pc := range content {
		for _, u := range pc.Units {
			if !strings.HasPrefix(u.ID, "policy/") {
				continue
			}
			if prev, ok := seen[u.ID]; ok {
				t.Fatalf("duplicate policy unit %q in %q and %q", u.ID, prev, pc.Pack.ID)
			}
			seen[u.ID] = pc.Pack.ID
		}
	}
	if len(seen) < 100 {
		t.Fatalf("expected many policy units, got %d", len(seen))
	}
}
