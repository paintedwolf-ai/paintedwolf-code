package migrations

import (
	"math"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedStorageAdmissionRequiresReviewedAndMeasuredRoute(t *testing.T) {
	plan := retainedStorageContractPlan()
	if !plan.reviewedRetainedRoute() {
		t.Fatal("registered route no longer matches the reviewed storage proof")
	}
	if plan.BoundedRetainedStorage() {
		t.Fatal("unmeasured route received bounded storage admission")
	}
	plan.retainedStoragePrepared = true
	plan.scratchBytes = 12345
	if !plan.BoundedRetainedStorage() {
		t.Fatal("measured reviewed route lost bounded storage admission")
	}
	again, err := plan.PrepareRetainedStorage(t.Context(), nil)
	testutil.FailErr(t, "reuse measured storage bound", err)
	if !again.BoundedRetainedStorage() || again.ScratchBytes() != 12345 {
		t.Fatal("repeated preparation changed the measured resource reservation")
	}
}

func TestRetainedStorageFallsBackForChangedRoute(t *testing.T) {
	changes := map[string]func(*Plan){
		"future step":      func(p *Plan) { p.steps = append(p.steps, Step{ID: "future-migration"}) },
		"missing step":     func(p *Plan) { p.steps = p.steps[:len(p.steps)-1] },
		"reordered steps":  func(p *Plan) { p.steps[1], p.steps[2] = p.steps[2], p.steps[1] },
		"different source": func(p *Plan) { p.Source.Shape = strings.Repeat("0", 64) },
		"future revision":  func(p *Plan) { p.Target.Revision++ },
		"different target": func(p *Plan) { p.Target.Shape = strings.Repeat("0", 64) },
		"no retained copy": func(p *Plan) { p.steps[0].RetainedCopy = nil },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			plan := retainedStorageContractPlan()
			change(&plan)
			assertRetainedStorageFallback(t, plan)
		})
	}
	for i, step := range retainedStorageContractPlan().steps {
		t.Run(step.ID, func(t *testing.T) {
			for _, field := range []string{"identity", "checksum"} {
				t.Run(field, func(t *testing.T) {
					plan := retainedStorageContractPlan()
					if field == "identity" {
						plan.steps[i].ID += "-changed"
					} else {
						plan.steps[i].Checksum = strings.Repeat("0", 64)
					}
					assertRetainedStorageFallback(t, plan)
				})
			}
		})
	}
}

func assertRetainedStorageFallback(t *testing.T, plan Plan) {
	t.Helper()
	if plan.reviewedRetainedRoute() {
		t.Fatal("changed route retained the reviewed resource proof")
	}
	prepared, err := plan.PrepareRetainedStorage(t.Context(), nil)
	testutil.FailErr(t, "preserve conservative route without measuring", err)
	if prepared.BoundedRetainedStorage() {
		t.Fatal("unknown route received bounded storage admission")
	}
	plan.retainedStoragePrepared = true
	if plan.BoundedRetainedStorage() {
		t.Fatal("stale preparation flag admitted a changed route")
	}
}

func retainedStorageContractPlan() Plan {
	target := Baseline{Revision: 11, Shape: "3a065b2adccf2628ce0e203a4e04962c974b462a7cae85e8996ba4e7696fe9e6"}
	links := SourceLinkKind(target)
	inline := ProgramInlineInputs(links.From)
	native := ProgramNativeGap(inline.From)
	programs := ProgramReceipts(native.From)
	identity := EffectPermitIdentity(programs.From)
	away := VaultUnlockAway(identity.From)
	manifest := SourceManifestLayout(away.From)
	return Plan{Source: manifest.From, Target: target, steps: []Step{manifest, away, identity, programs, native, inline, links}}
}

func TestRetainedSorterBudgetCoversMergeFanoutBoundaries(t *testing.T) {
	// Every record fits within the declared largest-record bound. These inputs
	// reach the row-count ceiling on runs, on either side of two merge levels.
	for _, tc := range []struct {
		name          string
		records, rows uint64
		wantWindows   uint64
	}{
		{name: "one full merge", records: 8 << 20, rows: 16, wantWindows: 2},
		{name: "second merge group", records: 8 << 20, rows: 17, wantWindows: 4},
		{name: "two full levels", records: 256 << 20, rows: 256, wantWindows: 18},
		{name: "third merge level", records: 256 << 20, rows: 257, wantWindows: 21},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sorterStorageBytes(tc.records, tc.rows, 1<<20, 4096)
			testutil.FailErr(t, "bound merge fanout", err)
			// Each oversized-record window includes the record's varint allowance.
			want := tc.records + 18*tc.rows + tc.wantWindows*((1<<20)+18) + (64 << 20)
			if got != want {
				t.Fatalf("sorter budget=%d, want %d for %d merge windows", got, want, tc.wantWindows)
			}
		})
	}
}

func TestRetainedSorterBudgetIncludesPageSizedMergeWindows(t *testing.T) {
	for _, tc := range []struct {
		pageBytes, wantWindow uint64
	}{
		{pageBytes: 512, wantWindow: 1024000},
		{pageBytes: 4096, wantWindow: 1024000},
		{pageBytes: 65536, wantWindow: 8192000},
	} {
		got, err := sorterStorageBytes(32, 1, 32, tc.pageBytes)
		testutil.FailErr(t, "bound page-sized merge window", err)
		want := uint64(32+18+(64<<20)) + tc.wantWindow
		if got != want {
			t.Fatalf("page size %d: sorter budget=%d, want %d", tc.pageBytes, got, want)
		}
	}
}

func TestRetainedSorterBudgetIncludesAlignedListRecords(t *testing.T) {
	// ROUND8(nVal + 16) can add seven bytes per record. At this boundary,
	// omitting alignment undercounts runs and drops an entire merge group.
	got, err := sorterStorageBytes(6192000, 100000, 128, 4096)
	testutil.FailErr(t, "bound aligned sorter records", err)
	const want = 79196864
	if got != want {
		t.Fatalf("aligned sorter budget=%d, want %d", got, want)
	}
}

func TestRetainedSorterBudgetRejectsMergeAndFinalSumOverflow(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		records, rows, largest uint64
	}{
		{name: "merge windows", records: 8 << 20, rows: 17, largest: math.MaxUint64 / 3},
		{name: "final reserve", records: math.MaxUint64 - (64 << 20), rows: 0, largest: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sorterStorageBytes(tc.records, tc.rows, tc.largest, 4096); err == nil {
				t.Fatal("accepted an overflowing scratch reservation")
			}
		})
	}
}
