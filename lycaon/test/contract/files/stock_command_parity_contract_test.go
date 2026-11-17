package contract

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const stockPlatformPrefix = "painted-wolf/platform:"

func TestEveryStockCommandIsReachable(t *testing.T) {
	t.Parallel()
	set := stockContributionSet(t)

	bound := map[string]bool{}
	for _, keybinding := range set.Keybindings() {
		bound[keybinding.Command] = true
	}
	placed := map[string]bool{}
	for _, menu := range set.Menus() {
		placed[menu.Command] = true
	}

	checked := 0
	for _, command := range set.Commands() {
		if !strings.HasPrefix(command.ID, stockPlatformPrefix) {
			continue
		}
		checked++
		if command.InPalette() || bound[command.ID] || placed[command.ID] || command.HostInvoked {
			continue
		}
		t.Errorf("stock command %s is unreachable", command.ID)
	}
	if checked == 0 {
		t.Fatalf("no stock commands under %s", stockPlatformPrefix)
	}
}

func stockCatalogView(t *testing.T) *catalogview.View {
	t.Helper()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "discover stock content", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   stock,
		Desired: extpacks.EmptyDesired(),
	})
	contractcheck.FailErr(t, "stock boot", eff.BootError())
	view, err := catalogview.Build(t.Context(), filepath.Join(contractcheck.RepoRoot(t), "lycaon"), eff)
	contractcheck.FailErr(t, "build stock view", err)
	return view
}

func stockContributionSet(t *testing.T) *contribution.Set {
	t.Helper()
	return stockCatalogView(t).Contributions
}

// A keybinding declares its own stratum, and the dispatcher ranks strata to
// resolve a shared chord. A binding whose stratum disagrees with its command's
// would rank under one scope and gate under another.
func TestStockKeybindingScopesMatchTheirCommands(t *testing.T) {
	t.Parallel()
	set := stockContributionSet(t)

	scopes := map[string]string{}
	for _, command := range set.Commands() {
		scopes[command.ID] = command.Scope
	}
	keybindings := set.Keybindings()
	if len(keybindings) == 0 {
		t.Fatal("no stock keybindings compiled")
	}
	for _, keybinding := range keybindings {
		want, ok := scopes[keybinding.Command]
		if !ok {
			t.Errorf("keybinding %s names no compiled command", keybinding.ID)
			continue
		}
		if want == "" {
			want = "global"
		}
		if keybinding.Scope != want {
			t.Errorf("keybinding %s is %s but %s is %s",
				keybinding.ID, keybinding.Scope, keybinding.Command, want)
		}
	}
}

// Stock keybindings cover every supported platform, with a chord or by
// leaving it to that platform's system menu bar.
func TestStockKeybindingsCoverEveryPlatform(t *testing.T) {
	t.Parallel()
	platforms := []string{"macos", "windows", "linux"}

	checked := 0
	for _, keybinding := range stockContributionSet(t).Keybindings() {
		if !strings.HasPrefix(keybinding.ID, stockPlatformPrefix) {
			continue
		}
		checked++
		for _, platform := range platforms {
			if len(keybinding.Bindings[platform]) == 0 && !slices.Contains(keybinding.SystemMenu, platform) {
				t.Errorf("keybinding %s declares no %s chord", keybinding.ID, platform)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no stock keybindings under %s", stockPlatformPrefix)
	}
}
