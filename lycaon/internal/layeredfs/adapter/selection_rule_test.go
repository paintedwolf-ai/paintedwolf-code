package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Costs here: an entry weighs 1000 bytes and the budget is 100000, so 3 entries cost 6000 and 60
// entries cost 120000; a 60000-byte file costs 120000 when it cannot be cloned and nothing when it can.
var ruleBudget = BudgetFact{Limit: 100_000, EntryCost: 1000, Provisional: true}

func ruleBudgetFor(class BudgetClass) BudgetFact {
	fact := ruleBudget
	fact.Class = class
	return fact
}

func writeSizedTree(t *testing.T, entries int, bytes int) string {
	t.Helper()
	root := t.TempDir()
	for i := range entries {
		size := 1
		if i == 0 {
			size = bytes
		}
		testutil.FailErr(t, "write tree", os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d", i)), make([]byte, size), 0o644))
	}
	return root
}

type mountOutcome struct {
	name   string
	reason ReasonCode
	absent bool
}

func ruleMount(outcome mountOutcome, calls *atomic.Int32) Adapter {
	if outcome.absent {
		return nil
	}
	return NewMountedAdapter(MountedAdapterConfig{
		AdapterID: AdapterFUSELinux, TargetOS: "linux",
		MountProbeFn: func(context.Context, string, string) (ReasonCode, string, error) {
			calls.Add(1)
			if outcome.reason == ReasonNone {
				return ReasonNone, "", nil
			}
			return outcome.reason, "fixture", errors.New("fixture")
		},
	})
}

func TestSelectionTableEveryCombination(t *testing.T) {
	mounts := []mountOutcome{
		{name: "mount-works"},
		{name: "mount-lasting-denial", reason: ReasonMountDenied},
		{name: "mount-confinement-unavailable", reason: ReasonConfinementUnavailable},
		{name: "mount-transient", reason: ReasonTransientStartupFailure},
		{name: "no-mount-here", absent: true},
	}
	for _, entries := range []int{3, 60} {
		for _, fileBytes := range []int{10, 60_000} {
			for _, clonable := range []bool{true, false} {
				for _, mount := range mounts {
					name := fmt.Sprintf("entries=%d/bytes=%d/clone=%v/%s", entries, fileBytes, clonable, mount.name)
					t.Run(name, func(t *testing.T) {
						assertSelectionRow(t, entries, fileBytes, clonable, mount)
					})
				}
			}
		}
	}
}

func assertSelectionRow(t *testing.T, entries, fileBytes int, clonable bool, mount mountOutcome) {
	t.Helper()
	var calls atomic.Int32
	sel := NewSelector("linux", ruleMount(mount, &calls), nil, NewEagerCopyAdapter(ReasonNone))
	sel.BudgetFor = ruleBudgetFor
	source := writeSizedTree(t, entries, fileBytes)
	choice, err := sel.Choose(t.Context(), ChooseRequest{
		Probe:          ProbeConfig{RuntimeDir: t.TempDir()},
		Grants:         []RootGrant{{RootID: "r", SourcePath: source, IsPrimary: true}},
		CloneAvailable: func(context.Context, RootGrant) (bool, error) { return clonable, nil },
	})

	overBudget := entries == 60 || (fileBytes == 60_000 && !clonable)
	switch {
	case !overBudget:
		testutil.FailErr(t, "Choose", err)
		if choice.Adapter.ID() != AdapterEagerCopy || choice.Facts.Reason != SelectionUnderBudget || choice.CopyBudget == nil {
			t.Fatalf("under budget must materialize with the budget enforced: %+v", choice)
		}
		if calls.Load() != 0 {
			t.Fatalf("under budget probed the mount %d times", calls.Load())
		}
	case mount.absent || IsLastingMountReason(mount.reason):
		wantReason := mount.reason
		if mount.absent {
			wantReason = ReasonUnsupportedPlatform
		}
		var aErr *AdapterError
		if !errors.As(err, &aErr) || aErr.ReasonCode != wantReason || choice.Adapter != nil {
			t.Fatalf("an unavailable mount must refuse over-limit work: choice=%+v err=%v", choice, err)
		}
		if !choice.Facts.DeclaredCost.Partial {
			t.Fatalf("bounded survey must not be replaced by a full fallback scan: %+v", choice.Facts.DeclaredCost)
		}
	case mount.reason == ReasonNone:
		testutil.FailErr(t, "Choose", err)
		if choice.Adapter.ID() != AdapterFUSELinux || choice.Facts.Reason != SelectionOverBudgetMounted || choice.CopyBudget != nil {
			t.Fatalf("over budget with a working mount must mount: %+v", choice)
		}
	default:
		var aErr *AdapterError
		if !errors.As(err, &aErr) || aErr.ReasonCode != ReasonTransientStartupFailure || choice.Adapter != nil {
			t.Fatalf("a transient failure must start no copy: choice=%+v err=%v", choice, err)
		}
		if calls.Load() != 2 {
			t.Fatalf("a transient failure retries once: %d probes", calls.Load())
		}
	}
}

func TestSelectionFromIndexFactsSurveysNothing(t *testing.T) {
	var calls atomic.Int32
	sel := NewSelector("linux", ruleMount(mountOutcome{}, &calls), nil, NewEagerCopyAdapter(ReasonNone))
	sel.BudgetFor = ruleBudgetFor
	missing := filepath.Join(t.TempDir(), "never-walked")
	choice, err := sel.Choose(t.Context(), ChooseRequest{
		Probe:          ProbeConfig{RuntimeDir: t.TempDir()},
		Grants:         []RootGrant{{RootID: "r", SourcePath: missing, IsPrimary: true, Estimate: &SourceIndexEstimate{EstimatedEntries: 100}}},
		CloneAvailable: func(context.Context, RootGrant) (bool, error) { return true, nil },
	})
	testutil.FailErr(t, "Choose", err)
	if choice.Facts.DeclaredCost.Source != CostFromIndex || choice.Adapter.ID() != AdapterFUSELinux {
		t.Fatalf("index facts must decide without a survey: %+v", choice.Facts)
	}
}

func TestSurveyStopsAtTheBudget(t *testing.T) {
	var calls atomic.Int32
	sel := NewSelector("linux", ruleMount(mountOutcome{}, &calls), nil, NewEagerCopyAdapter(ReasonNone))
	sel.BudgetFor = ruleBudgetFor
	source := writeSizedTree(t, 200, 1)
	choice, err := sel.Choose(t.Context(), ChooseRequest{
		Probe:          ProbeConfig{RuntimeDir: t.TempDir()},
		Grants:         []RootGrant{{RootID: "r", SourcePath: source, IsPrimary: true}},
		CloneAvailable: func(context.Context, RootGrant) (bool, error) { return true, nil },
	})
	testutil.FailErr(t, "Choose", err)
	// 51 entries cost 102000, the first count past 100000.
	if got := choice.Facts.DeclaredCost; !got.Partial || got.Entries != 51 {
		t.Fatalf("survey should stop at the budget: %+v", got)
	}
}

func TestDiskThatCannotHoldTheResultRefusesWithTheNumber(t *testing.T) {
	var calls atomic.Int32
	sel := NewSelector("linux", ruleMount(mountOutcome{reason: ReasonMountDenied}, &calls), nil, NewEagerCopyAdapter(ReasonNone))
	sel.BudgetFor = ruleBudgetFor
	source := writeSizedTree(t, 3, 100)
	testutil.FailErr(t, "subdir", os.Mkdir(filepath.Join(source, "sub"), 0o755))
	_, err := sel.Choose(t.Context(), ChooseRequest{
		Probe:  ProbeConfig{RuntimeDir: t.TempDir()},
		Grants: []RootGrant{{RootID: "r", SourcePath: source, IsPrimary: true}},
		Disk:   func() (DiskSpace, error) { return DiskSpace{Path: "/data", Available: 1000, Known: true}, nil },
	})
	var capErr *WorkspaceCapacityError
	if !errors.As(err, &capErr) || capErr.Available != 1000 || capErr.Required <= capErr.Available {
		t.Fatalf("expected a refusal naming both numbers, got %+v (%v)", capErr, err)
	}
	if len(capErr.SuggestedSubroots) != 1 || capErr.SuggestedSubroots[0] != "r:sub" {
		t.Fatalf("expected a concrete smaller workspace, got %v", capErr.SuggestedSubroots)
	}
}

func TestAStaleEstimateOverrunsAndMovesOnToTheMount(t *testing.T) {
	var calls atomic.Int32
	sel := NewSelector("linux", ruleMount(mountOutcome{}, &calls), nil, NewEagerCopyAdapter(ReasonNone))
	sel.BudgetFor = ruleBudgetFor
	source := writeSizedTree(t, 60, 1)
	grants := []RootGrant{{RootID: "r", SourcePath: source, IsPrimary: true, Estimate: &SourceIndexEstimate{EstimatedEntries: 1}}}
	first, err := sel.Choose(t.Context(), ChooseRequest{Probe: ProbeConfig{RuntimeDir: t.TempDir()}, Grants: grants})
	testutil.FailErr(t, "first Choose", err)
	if first.Adapter.ID() != AdapterEagerCopy {
		t.Fatalf("the estimate says under budget: %+v", first.Facts)
	}

	runtimeDir, target := t.TempDir(), filepath.Join(t.TempDir(), "target")
	sibling := NewEagerCopyAdapter(ReasonNone)
	kept, err := sibling.Prepare(t.Context(), PrepareRequest{
		ViewID: "kept-view", RuntimeDir: runtimeDir, Grants: []RootGrant{{RootID: "s", SourcePath: writeSizedTree(t, 1, 1), IsPrimary: true}},
	})
	testutil.FailErr(t, "sibling Prepare", err)
	_, err = first.Adapter.Prepare(t.Context(), PrepareRequest{
		ViewID: "overrun-view", RuntimeDir: runtimeDir, Grants: grants, CopyBudget: first.CopyBudget,
		TargetRootDirs: map[string]string{"r": target}, CloneFile: copyingClone,
	})
	var overrun *BudgetOverrunError
	if !errors.As(err, &overrun) || overrun.Entries != 51 {
		t.Fatalf("expected the copy to stop at the budget, got %+v (%v)", overrun, err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("the overrun tree remains: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(kept.PrimaryWorkPath, "f000")); statErr != nil {
		t.Fatalf("an overrun touched another view: %v", statErr)
	}

	second, err := sel.Choose(t.Context(), ChooseRequest{Probe: ProbeConfig{RuntimeDir: t.TempDir()}, Grants: grants, Overrun: overrun})
	testutil.FailErr(t, "second Choose", err)
	if second.Adapter.ID() != AdapterFUSELinux || !second.Facts.CopyPassedBudget {
		t.Fatalf("selection must move on to the mount: %+v", second.Facts)
	}
}

func TestRecordedBudgetsAreOneProvisionalFactPerClass(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "elsewhere"} {
		for _, fs := range []FilesystemClass{FilesystemClonable, FilesystemCopyOnly} {
			fact := RecordedBudget(BudgetClass{Platform: platform, Filesystem: fs})
			if fact.Limit <= 0 || fact.EntryCost <= 0 || !fact.Provisional {
				t.Fatalf("%s/%s: %+v", platform, fs, fact)
			}
		}
	}
}
