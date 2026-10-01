package extpacks

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPrepareMetaMemberMutationMissingMembersRejectsEnable(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	stageStockNamespace(t, map[string]map[string]string{
		"pw/a": stockUnits("pw/a"),
	}, MetaPackManifest{
		ID: StockMetaPackID, Name: "Stock", Version: "1.0.0",
		Members: []string{"pw/a", "pw/missing"},
	})
	_, err := PrepareMetaMemberMutation(StockMetaPackID, true)
	if !errors.Is(err, ErrMissingMembers) {
		t.Fatalf("want ErrMissingMembers, got %v", err)
	}
}

func TestPrepareMetaMemberMutationRefusesDisablingTheStockSuite(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	stageStockNamespace(t, map[string]map[string]string{
		"pw/a": stockUnits("pw/a"),
		"pw/b": stockUnits("pw/b"),
	}, MetaPackManifest{
		ID: StockMetaPackID, Name: "Stock", Version: "1.0.0",
		Members: []string{"pw/a", "pw/b"},
	})
	_, err := PrepareMetaMemberMutation(StockMetaPackID, false)
	if !errors.Is(err, ErrStockMetaPack) {
		t.Fatalf("want ErrStockMetaPack, got %v", err)
	}
}

func TestPrepareMetaMemberMutationDisableWarnsAndDisablesPresent(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	stageStockNamespace(t, map[string]map[string]string{
		"pw/a": stockUnits("pw/a"),
		"pw/b": stockUnits("pw/b"),
	}, MetaPackManifest{
		ID: "acme/kit", Name: "Kit", Version: "1.0.0",
		Members: []string{"pw/a", "pw/b", "pw/missing"},
	})

	mutation, err := PrepareMetaMemberMutation("acme/kit", false)
	testutil.FailErr(t, "PrepareMetaMemberMutation", err)
	if len(mutation.Warnings) == 0 || !strings.Contains(mutation.Warnings[0], "pw/missing") {
		t.Fatalf("expected missing warning, got %v", mutation.Warnings)
	}
	desired := mutation.ApplyTo(EmptyDesired())
	if PackEnabled(desired, "pw/a") || PackEnabled(desired, "pw/b") {
		t.Fatalf("members should be disabled: %+v", desired.Packs)
	}
}

// Missing flag-only rows do not represent installed packs.
func TestPrepareMetaMemberMutationApplyToPrunesStaleFlagOnlyRowsForMissingMembers(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	stageStockNamespace(t, map[string]map[string]string{
		"pw/a": stockUnits("pw/a"),
	}, MetaPackManifest{
		ID: "acme/kit", Name: "Kit", Version: "1.0.0",
		Members: []string{"pw/a", "pw/missing-flag-only", "pw/missing-installed"},
	})

	mutation, err := PrepareMetaMemberMutation("acme/kit", false)
	testutil.FailErr(t, "PrepareMetaMemberMutation", err)

	enabled := false
	desired := EmptyDesired()
	desired = setPackRow(desired, DesiredPack{ID: "pw/missing-flag-only", Enabled: &enabled})
	desired = setPackRow(desired, DesiredPack{ID: "pw/missing-installed", Source: "github.com/acme/missing", Enabled: &enabled})

	desired = mutation.ApplyTo(desired)

	if _, ok := DesiredPackRow(desired, "pw/missing-flag-only"); ok {
		t.Fatal("stale flag-only row for a missing member should be pruned")
	}
	row, ok := DesiredPackRow(desired, "pw/missing-installed")
	if !ok {
		t.Fatal("a row naming a real install source must never be pruned")
	}
	if row.Source != "github.com/acme/missing" {
		t.Fatalf("installed row was mutated: %+v", row)
	}
}

func TestRemoveMetaPackStock(t *testing.T) {
	err := RemoveMetaPack(StockMetaPackID)
	if !errors.Is(err, ErrStockMetaPack) {
		t.Fatalf("want ErrStockMetaPack, got %v", err)
	}
}
