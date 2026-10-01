package extpacks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestApplyProfileClassifiesEveryUnitKind(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)

	packDir := filepath.Join(t.TempDir(), "basic")
	if err := os.MkdirAll(filepath.Join(packDir, "profiles"), 0o700); err != nil {
		testutil.FailErr(t, "mkdir pack", err)
	}
	manifest := "manifest_version: 1\nid: acme/basic\nname: Acme\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n"
	if err := os.WriteFile(filepath.Join(packDir, "extension.yaml"), []byte(manifest), 0o600); err != nil {
		testutil.FailErr(t, "write manifest", err)
	}
	profile := `name: lockdown
disable:
  - policy/SOME_CODE
  - guidance/some-note
  - workflows/plan
  - agents/plan-writer
  - host/bindings/inject-blueprint
  - tools/profiles/plan_write_only
  - approvals/some-key
  - playbooks/some-book
  - shared/archetypes/plan_write
  - host/user-notices/session_aborted
  - mcp_bindings/fixture_widget
  - skills/verify-a-change
  - contributions/commands/explain-selection
  - painted-wolf/options
  - acme/other
`
	if err := os.WriteFile(filepath.Join(packDir, "profiles", "lockdown.yaml"), []byte(profile), 0o600); err != nil {
		testutil.FailErr(t, "write profile", err)
	}

	loaded, err := LoadProfile(packDir, "lockdown")
	testutil.FailErr(t, "load profile", err)
	seed := EmptyDesired()
	seed = setPackRow(seed, DesiredPack{ID: "acme/basic", Enabled: boolPtr(true)})
	desired := loaded.ApplyTo(seed)

	wantDisabled := []string{
		"policy/SOME_CODE", "guidance/some-note", "workflows/plan", "agents/plan-writer",
		"host/bindings/inject-blueprint", "tools/profiles/plan_write_only", "approvals/some-key",
		"playbooks/some-book", "shared/archetypes/plan_write", "host/user-notices/session_aborted",
		"mcp_bindings/fixture_widget",
		"skills/verify-a-change",
		"contributions/commands/explain-selection",
	}
	got := map[string]bool{}
	for _, id := range desired.Disabled {
		got[id] = true
	}
	for _, id := range wantDisabled {
		if !got[id] {
			t.Errorf("unit id %q missing from disabled: %v", id, desired.Disabled)
		}
	}

	wantPacks := map[string]bool{"acme/basic": true, "painted-wolf/options": true, "acme/other": true}
	for _, p := range desired.Packs {
		if !wantPacks[p.ID] {
			t.Errorf("unit id %q written as a pack row, want disabled entry", p.ID)
		}
	}
	if len(desired.Packs) != len(wantPacks) {
		t.Errorf("packs = %d rows, want %d", len(desired.Packs), len(wantPacks))
	}
}
