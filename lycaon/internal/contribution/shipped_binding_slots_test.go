package contribution

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const stockCollisionFault = "collides with another stock keybinding"

// shippedContributionUnits loads every bundled pack's contribution units.
func shippedContributionUnits(t *testing.T) ([]Input, map[string]string) {
	t.Helper()
	root := filepath.Join("..", "..", "config", "packs")
	var units []Input
	providers := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() || entry.Name() != "contributions" {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		packID := filepath.ToSlash(rel)
		for _, kind := range Kinds() {
			kindDir := filepath.Join(path, string(kind))
			entries, err := os.ReadDir(kindDir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			for _, file := range entries {
				if file.IsDir() || filepath.Ext(file.Name()) != ".yaml" {
					continue
				}
				unitPath := filepath.Join(kindDir, file.Name())
				body, err := os.ReadFile(unitPath) // #nosec G304 -- bundled pack path under test
				if err != nil {
					return err
				}
				unitID := "contributions/" + string(kind) + "/" + strings.TrimSuffix(file.Name(), ".yaml")
				providers[unitID] = packID
				units = append(units, Input{
					UnitID: unitID, Kind: kind, ProviderPackID: packID, Body: body, Origin: unitPath,
				})
			}
		}
		return fs.SkipDir
	})
	if err != nil {
		testutil.FailErr(t, "walk bundled packs", err)
	}
	return units, providers
}

// Every bundled keybinding in every pack must own its platform/scope/chord slot.
func TestEveryShippedPackBindingOwnsItsChordSlot(t *testing.T) {
	t.Parallel()

	units, providers := shippedContributionUnits(t)
	keybindings := 0
	for _, unit := range units {
		if unit.Kind == KindKeybinding {
			keybindings++
		}
	}
	if keybindings == 0 {
		t.Fatal("no shipped keybinding units found under config/packs; the walk is looking in the wrong place")
	}

	set, err := Compile(CompileInput{
		Units: units,
		// Prompt refs resolve in the full catalog; this test only needs the slots.
		UnitProvider: func(unitID string) (string, bool) {
			if provider, ok := providers[unitID]; ok {
				return provider, true
			}
			return "painted-wolf/platform", true
		},
		PackPresent:  func(string) bool { return true },
		ProviderRank: func(string) ProviderRank { return RankStock },
	})
	const fix = "rule: two stock keybindings may not share a platform, scope, and chord " +
		"(a tie ships a dead chord); fix: rebind one candidate in its " +
		"config/packs/**/contributions/keybindings/*.yaml"
	var compileErr *CompileError
	if errors.As(err, &compileErr) {
		for _, fault := range compileErr.Faults {
			if strings.Contains(fault.Message, stockCollisionFault) {
				t.Errorf("%s (%s): %s\n%s", fault.PackID, fault.UnitID, fault.Message, fix)
				continue
			}
			t.Errorf("shipped contribution fault %s (%s) %s: %s", fault.PackID, fault.UnitID, fault.Code, fault.Message)
		}
		return
	}
	if err != nil {
		testutil.FailErr(t, "compile shipped contributions", err)
	}
	for _, note := range set.Notes() {
		if note.Code == NoteBindingConflict {
			t.Errorf("shipped binding collision: %s\n%s", note.Message, fix)
		}
	}
	for _, def := range set.BindingDefaults() {
		if def.Active == nil {
			t.Errorf("shipped %s %s in scope %s has no active binding (candidates %s)\n%s",
				def.Platform, def.Chord, def.Scope, joinIDList(def.Candidates), fix)
		}
	}
}

func joinIDList(ids []ID) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return strings.Join(out, ", ")
}
