package main

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
)

// upgradeMeasured is store pages and pages fetched from the full-size run.
var upgradeMeasured = map[int64][2]int64{50_000: {6_942, 248_632}, 500_000: {61_321, 2_793_798}}

// healthyScale is shaped like a real run: boot work equal at both sizes,
// linear deletes in bounded statements, sliced vacuums, linear upgrades.
func healthyScale() (scaleMeasurement, scaleMeasurement) {
	size := func(name string, files int64) scaleMeasurement {
		free := files * 2
		owned := files * 5
		return scaleMeasurement{
			Size:    scaleSize{Name: name, Files: int(files), FreePages: free},
			Fixture: scaleFixture{PageSize: 4096},
			Boot:    scaleBoot{FilesOpened: 1, Work: map[string]int64{"statements": 300, "pages_fetched": 1017, "rows_returned": 2829}},
			Vacuum: scaleVacuum{
				FreePagesBefore: free, FreePagesAfter: 200, PassPages: db.VacuumPassPages(free, 4096),
				Passes: free/db.VacuumPassPages(free, 4096) + 2, VacuumPasses: free / db.VacuumPassPages(free, 4096),
				Turns: free/db.VacuumPassPages(free, 4096) + 3, PeakPassPages: db.VacuumPassPages(free, 4096), PagesReclaimed: free - 200,
			},
			Delete: scaleDelete{OwnedRows: owned, Work: map[string]int64{
				"statements": owned/1024 + 70, "pages_fetched": owned * 20, "rows_changed": owned * 3 / 2,
				"peak_statement_rows_changed": 4096, "peak_transaction_rows_changed": 4096, "peak_statement_rows_returned": 64,
			}},
			Upgrade: scaleUpgrade{Entries: int(files), StorePages: upgradeMeasured[files][0], Work: map[string]int64{
				"pages_fetched": upgradeMeasured[files][1], "peak_statement_rows_returned": 640, "peak_resident_bytes": 180 << 20,
			}},
		}
	}
	return size("small", 50_000), size("large", 500_000)
}

func TestHealthyScaleMeetsEveryBound(t *testing.T) {
	small, large := healthyScale()
	if violations := evaluateScale(small, large); len(violations) != 0 {
		t.Fatalf("violations = %v", violations)
	}
}

func TestScaleBoundsRefuseEachUnboundedShape(t *testing.T) {
	for _, fault := range []struct {
		name   string
		inject func(small, large *scaleMeasurement)
		want   string
	}{
		{"boot scans a table in full", func(small, large *scaleMeasurement) {
			small.Boot.Work["pages_fetched"] += 40
			large.Boot.Work["pages_fetched"] += 400
		}, boundBootPagesSpread},
		{"boot walks the project tree", func(small, large *scaleMeasurement) {
			small.Boot.FilesOpened, large.Boot.FilesOpened = 300, 3_000
		}, boundBootFilesOpened},
		{"delete loads every file row", func(small, large *scaleMeasurement) {
			small.Delete.Work["peak_statement_rows_returned"] = 50_000
			large.Delete.Work["peak_statement_rows_returned"] = 500_000
		}, boundDeleteStepRows},
		{"delete runs as one statement", func(small, large *scaleMeasurement) {
			large.Delete.Work["peak_statement_rows_changed"] = large.Delete.OwnedRows
		}, boundDeleteStepRows},
		{"delete statements grow with the square of its rows", func(small, large *scaleMeasurement) {
			small.Delete.Work["pages_fetched"] = small.Delete.OwnedRows * 26
			large.Delete.Work["pages_fetched"] = large.Delete.OwnedRows * 260
		}, boundDeleteGrowth},
		{"vacuum frees the whole freelist in one pass", func(small, large *scaleMeasurement) {
			large.Vacuum.PeakPassPages, large.Vacuum.VacuumPasses = large.Vacuum.FreePagesBefore, 1
		}, boundVacuumPassPages},
		{"vacuum takes passes without turns", func(small, large *scaleMeasurement) {
			large.Vacuum.Turns = 1
		}, boundVacuumTurns},
		{"upgrade reads the store over and over", func(small, large *scaleMeasurement) {
			large.Upgrade.Work["pages_fetched"] = upgradeBasePages + large.Upgrade.StorePages*400
		}, boundUpgradePages},
		{"upgrade holds the whole store in memory", func(small, large *scaleMeasurement) {
			large.Upgrade.Work["peak_resident_bytes"] += 512 << 20
		}, boundUpgradeResident},
	} {
		t.Run(fault.name, func(t *testing.T) {
			small, large := healthyScale()
			fault.inject(&small, &large)
			violations := evaluateScale(small, large)
			if !slices.ContainsFunc(violations, func(v scaleViolation) bool { return v.Bound == fault.want }) {
				t.Fatalf("violations = %v, want bound %s", violations, fault.want)
			}
		})
	}
}
