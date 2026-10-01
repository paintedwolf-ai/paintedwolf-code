package catalogview_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialCatalogViewLifecycle(t *testing.T) {
	const path = "host/credential-slots/access.yaml"
	pack := fixturePack(t, "acme/access", map[string]string{path: "version: 1\nenv_keys: [ACCESS_VALUE]"})
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), nil)
	eff := resolveWithFixtures(t, pack)
	view, err := cache.For(t.Context(), eff)
	testutil.FailErr(t, "compile initial view", err)
	args := map[string]any{"ACCESS_VALUE": "one", "NEW_VALUE": "two", "password": "three"}
	if hits := view.CredentialSlots.Inspect("external", args); len(hits) != 2 {
		t.Fatalf("initial recognition = %d", len(hits))
	}
	withRules := view.WithRules(oar.NewRuleSet(nil))
	if withRules.CredentialSlots != view.CredentialSlots {
		t.Fatal("project OAR configuration lost recognition")
	}
	updated := fixturePack(t, "acme/access", map[string]string{path: "version: 1\nenv_keys: [NEW_VALUE]"})
	next, err := cache.For(t.Context(), resolveWithFixtures(t, updated))
	testutil.FailErr(t, "compile updated view", err)
	if next == view || len(next.CredentialSlots.Inspect("external", map[string]any{"ACCESS_VALUE": "one"})) != 0 || len(next.CredentialSlots.Inspect("external", map[string]any{"NEW_VALUE": "two"})) != 1 {
		t.Fatal("updated catalog retained stale recognition")
	}
	removed, err := cache.For(t.Context(), resolveWithFixtures(t))
	testutil.FailErr(t, "compile removed view", err)
	if hits := removed.CredentialSlots.Inspect("external", args); len(hits) != 1 {
		t.Fatal("removal must retain only the bundled baseline")
	}
	if len(view.CredentialSlots.Inspect("external", args)) != 2 {
		t.Fatal("new revisions mutated an admitted view")
	}
}

func TestCredentialCatalogRejectsCandidateAndIsolatesCommittedFault(t *testing.T) {
	good := fixturePack(t, "acme/good", map[string]string{"host/credential-slots/access.yaml": "version: 1\nenv_keys: [ACCESS_VALUE]"})
	bad := fixturePack(t, "acme/bad", map[string]string{"host/credential-slots/access.yaml": "version: 1\nexcluded_keys: [password]"})
	eff := resolveWithFixtures(t, good, bad)
	cache := catalogview.NewCache(configlayout.FindModuleRoot(), nil)
	_, _, err := cache.ForCandidate(t.Context(), eff, []string{"acme/bad"})
	var compileErr *contribution.CompileError
	if !errors.As(err, &compileErr) || len(compileErr.Faults) != 1 || compileErr.Faults[0].PackID != "acme/bad" || compileErr.Faults[0].Code != extpacks.DiagCredentialSlotsInvalid {
		t.Fatalf("candidate rejection lost attribution: %v", err)
	}
	view, committed, err := cache.ForCommitted(t.Context(), eff)
	testutil.FailErr(t, "isolate invalid committed pack", err)
	if committed.PackContributed("acme/bad") || !committed.PackContributed("acme/good") {
		t.Fatal("committed-state isolation held out the wrong pack")
	}
	if hits := view.CredentialSlots.Inspect("external", map[string]any{"ACCESS_VALUE": "one", "password": "two"}); len(hits) != 2 {
		t.Fatal("isolation lost valid recognition")
	}
}
