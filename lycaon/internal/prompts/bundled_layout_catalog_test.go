package prompts

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

// stageTwoPacks creates a bundled guidance collision.
func stageTwoPacks(t *testing.T) {
	t.Helper()
	files := map[config.Rel]string{}
	for _, leaf := range []string{"stockish", "fork"} {
		pack := config.Pack(leaf)
		files[pack.Join(config.PackManifestName)] = "manifest_version: 1\nid: painted-wolf/" + leaf +
			"\nname: " + leaf + "\nversion: 1.0.0\ncompatibility:\n  extension_api: \"^1.0.0\"\n"
		files[pack.Join("guidance", "dup.md")] = "body from " + leaf + "\n"
		files[pack.Join("guidance", leaf+"-only.md")] = "only " + leaf + "\n"
	}
	configtest.Overlay(t, files)
}

// An `own:` entry resolves a guidance collision.
func TestResolveBundledHonorsOwn(t *testing.T) {
	stageTwoPacks(t)
	t.Cleanup(extpacks.ClearActive)

	desired := extpacks.EmptyDesired()
	desired.Own = map[string]string{"guidance/dup": "painted-wolf/fork"}
	eff, err := extpackstest.Resolve(t.Context(), desired, nil)
	testutil.FailErr(t, "resolve", err)
	extpacks.SetActive(eff)

	body, at, err := DefaultBundledLayout().ReadBundled("guidance/dup")
	testutil.FailErr(t, "resolve guidance/dup", err)
	if filepath.Base(at.Parent().Parent().String()) != "fork" {
		t.Fatalf("own winner not used: %s", at)
	}
	if !strings.Contains(string(body), "body from fork") {
		t.Fatalf("own winner bytes not served: %q", body)
	}
}

// Disabled units return their structured catalog error.
func TestResolveBundledRefusesDisabledUnit(t *testing.T) {
	stageTwoPacks(t)
	t.Cleanup(extpacks.ClearActive)

	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{"guidance/fork-only"}
	eff, err := extpackstest.Resolve(t.Context(), desired, nil)
	testutil.FailErr(t, "resolve", err)
	extpacks.SetActive(eff)

	_, _, err = DefaultBundledLayout().ReadBundled("guidance/fork-only")
	if !errors.Is(err, ErrTemplateNotEffective) {
		t.Fatalf("err = %v, want ErrTemplateNotEffective", err)
	}

	if _, _, err := DefaultBundledLayout().ReadBundled("guidance/stockish-only"); err != nil {
		t.Fatalf("sibling unit must still resolve: %v", err)
	}
}

// Prompt layers honor effective-catalog filtering.
func TestPromptLayersDoNotServeDisabledUnit(t *testing.T) {
	stageTwoPacks(t)
	t.Cleanup(extpacks.ClearActive)

	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{"guidance/fork-only"}
	eff, err := extpackstest.Resolve(t.Context(), desired, nil)
	testutil.FailErr(t, "resolve", err)
	extpacks.SetActive(eff)

	layers := PromptLayers{}
	if _, err := layers.ReadFile("guidance/fork-only"); !errors.Is(err, ErrTemplateNotEffective) {
		t.Fatalf("err = %v, want ErrTemplateNotEffective", err)
	}
}
