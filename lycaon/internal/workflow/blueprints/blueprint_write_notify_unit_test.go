package blueprints

import (
	"testing"
)

func TestMintBlueprintTitle(t *testing.T) {
	if got := mintBlueprintTitle("plan", "auth-fix"); got != "auth-fix" {
		t.Fatalf("got %q", got)
	}
	if got := mintBlueprintTitle("Ship auth", ""); got != "Ship auth" {
		t.Fatalf("got %q", got)
	}
	if got := mintBlueprintTitle("", ""); got != "blueprint" {
		t.Fatalf("got %q", got)
	}
	if got := mintBlueprintTitle("blueprint", "auth-fix"); got != "auth-fix" {
		t.Fatalf("got %q", got)
	}
}
