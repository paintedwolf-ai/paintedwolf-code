package sandbox

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"testing"
)

func TestProfileCommandEffectsAreExplicitClosedDeclarations(t *testing.T) {
	profile, err := ParseToolProfile([]byte("id: fixture\ncommand_effects: [read_only]\n"), nil)
	testutil.FailErr(t, "parse explicit read-only effects", err)
	read, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	write, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	if !profile.CommandEffects.Allows(read) || profile.CommandEffects.Allows(write) {
		t.Fatal("profile effect declaration widened")
	}
	absent, err := ParseToolProfile([]byte("id: legacy\n"), nil)
	testutil.FailErr(t, "parse existing profile", err)
	if absent.CommandEffects.Allows(read) {
		t.Fatal("missing new declaration enabled command")
	}
	for _, text := range []string{"id: fixture\ncommand_effects: [unknown]\n", "id: fixture\ncommand_effects: [read_only, read_only]\n"} {
		if _, err := ParseToolProfile([]byte(text), nil); err == nil {
			t.Fatalf("invalid effect declaration admitted %s", text)
		}
	}
}
