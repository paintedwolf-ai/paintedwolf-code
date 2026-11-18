package main

import (
	"fmt"
	"math"

	"github.com/lycaon/lycaon/internal/db"
)

// Declared work bounds. Every bound is a count; none is a duration.
const (
	// Boot does the same work on a small store and a large one, within these
	// spreads; a deeper B-tree may add a page to a lookup.
	bootPagesSpread      = 256
	bootStatementsSpread = 8
	bootRowsSpread       = 64
	// Boot opens the project root once and nothing else in the tree.
	bootFilesOpened = 1

	// Deleting a project changes at most this many rows per row it owns,
	// counting the rows triggers change.
	deleteRowsPerOwnedRow = 4
	// No statement or transaction of a delete changes or returns more rows.
	deleteStepRows = 16_384

	// An upgrade fetches at most a fixed base of pages, which validating the
	// schema costs on any store, plus this many per page in the store. Index
	// inserts descend a tree that deepens with size, so the rate grows slowly.
	upgradeBasePages         = 65_536
	upgradePagesPerStorePage = 64
	// No upgrade statement returns more rows.
	upgradeStepRows = 4_096
	// An upgrade's peak resident memory differs between sizes by at most this.
	upgradeResidentSpread = 64 << 20

	// Work per unit at the large size is at most this multiple of the small
	// size's, which a quadratic factor exceeds by ten times the size ratio.
	scaleGrowth = 2.0
)

// Bound names, as reported and as keyed in the report's bounds.
const (
	boundBootFilesOpened  = "boot_files_opened"
	boundBootPagesSpread  = "boot_pages_spread"
	boundBootStatements   = "boot_statements_spread"
	boundBootRowsSpread   = "boot_rows_spread"
	boundVacuumPassPages  = "vacuum_pass_pages"
	boundVacuumTurns      = "vacuum_turns"
	boundVacuumReclaims   = "vacuum_reclaims"
	boundDeleteOwnsRows   = "delete_owns_rows"
	boundDeleteRows       = "delete_rows_per_owned_row"
	boundDeleteStepRows   = "delete_step_rows"
	boundDeleteGrowth     = "delete_growth"
	boundUpgradePages     = "upgrade_pages"
	boundUpgradeStepRows  = "upgrade_step_rows"
	boundUpgradeRate      = "upgrade_pages_per_store_page"
	boundUpgradeResident  = "upgrade_resident_spread"
	boundUpgradeMeasured  = "upgrade_resident_measured"
	boundBootFilesSpread  = "boot_files_spread"
	boundUpgradeBasePages = "upgrade_base_pages"
	boundGrowth           = "growth"
	boundVacuumPasses     = "vacuum_pass_count"
)

// scaleViolation is one bound a measurement broke.
type scaleViolation struct {
	Size   string `json:"size,omitempty"`
	Bound  string `json:"bound"`
	Detail string `json:"detail"`
}

func (v scaleViolation) String() string {
	if v.Size == "" {
		return v.Bound + ": " + v.Detail
	}
	return v.Size + " " + v.Bound + ": " + v.Detail
}

func scaleBounds() map[string]float64 {
	return map[string]float64{
		boundBootPagesSpread: bootPagesSpread, boundBootStatements: bootStatementsSpread,
		boundBootRowsSpread: bootRowsSpread, boundBootFilesOpened: bootFilesOpened,
		boundDeleteRows: deleteRowsPerOwnedRow, boundDeleteStepRows: deleteStepRows,
		boundUpgradeBasePages: upgradeBasePages, boundUpgradeRate: upgradePagesPerStorePage, boundUpgradeStepRows: upgradeStepRows,
		boundUpgradeResident: upgradeResidentSpread, boundVacuumPassPages: float64(db.VacuumPassShrinkLimit(4096)),
		boundGrowth: scaleGrowth,
	}
}

type violations []scaleViolation

func (vs *violations) check(size, bound string, ok bool, format string, args ...any) {
	if !ok {
		*vs = append(*vs, scaleViolation{Size: size, Bound: bound, Detail: fmt.Sprintf(format, args...)})
	}
}

// evaluateScale checks every bound on one size, then the spreads and growth
// between the small and the large size.
func evaluateScale(small, large scaleMeasurement) []scaleViolation {
	var vs violations
	evaluateSize(&vs, small)
	evaluateSize(&vs, large)
	vs.check("", boundBootFilesSpread, large.Boot.FilesOpened == small.Boot.FilesOpened,
		"boot opened %d project files at the large size and %d at the small", large.Boot.FilesOpened, small.Boot.FilesOpened)
	for _, spread := range []struct {
		bound, counter string
		limit          int64
	}{{boundBootPagesSpread, "pages_fetched", bootPagesSpread}, {boundBootStatements, "statements", bootStatementsSpread}, {boundBootRowsSpread, "rows_returned", bootRowsSpread}} {
		difference := large.Boot.Work[spread.counter] - small.Boot.Work[spread.counter]
		vs.check("", spread.bound, difference <= spread.limit, "boot %s grew by %d between sizes, above %d", spread.counter, difference, spread.limit)
	}
	for _, counter := range []string{"statements", "pages_fetched", "rows_changed"} {
		smallRate := perUnit(small.Delete.Work[counter], small.Delete.OwnedRows)
		largeRate := perUnit(large.Delete.Work[counter], large.Delete.OwnedRows)
		vs.check("", boundDeleteGrowth, largeRate <= smallRate*scaleGrowth,
			"delete %s per owned row grew from %.3f to %.3f between sizes", counter, smallRate, largeRate)
	}
	smallRate := perUnit(small.Upgrade.Work["pages_fetched"]-upgradeBasePages, small.Upgrade.StorePages)
	largeRate := perUnit(large.Upgrade.Work["pages_fetched"]-upgradeBasePages, large.Upgrade.StorePages)
	vs.check("", boundUpgradeRate, largeRate <= max(smallRate, 1)*scaleGrowth,
		"upgrade pages fetched per store page, beyond the base, grew from %.3f to %.3f between sizes", smallRate, largeRate)
	resident := large.Upgrade.Work["peak_resident_bytes"] - small.Upgrade.Work["peak_resident_bytes"]
	vs.check("", boundUpgradeResident, resident <= upgradeResidentSpread,
		"upgrade peak resident memory grew by %d bytes between sizes, above %d", resident, upgradeResidentSpread)
	return vs
}

func evaluateSize(vs *violations, m scaleMeasurement) {
	size := m.Size.Name
	vs.check(size, boundBootFilesOpened, m.Boot.FilesOpened >= 0 && m.Boot.FilesOpened <= bootFilesOpened,
		"boot opened %d project files, above %d", m.Boot.FilesOpened, bootFilesOpened)

	v := m.Vacuum
	vs.check(size, boundVacuumPassPages, v.PeakPassPages <= db.VacuumPassShrinkLimit(m.Fixture.PageSize),
		"a vacuum pass removed %d pages from the file, above %d", v.PeakPassPages, db.VacuumPassShrinkLimit(m.Fixture.PageSize))
	passLimit := ceilDiv(v.FreePagesBefore, v.PassPages) + 1
	vs.check(size, boundVacuumPasses, v.PassPages > 0 && v.VacuumPasses <= passLimit,
		"vacuum took %d passes for %d free pages, above %d", v.VacuumPasses, v.FreePagesBefore, passLimit)
	vs.check(size, boundVacuumTurns, v.Turns >= v.Passes, "vacuum ran %d passes in %d turns", v.Passes, v.Turns)
	vs.check(size, boundVacuumReclaims, v.PagesReclaimed > 0 && v.FreePagesAfter < v.FreePagesBefore,
		"vacuum reclaimed %d pages, leaving %d of %d free", v.PagesReclaimed, v.FreePagesAfter, v.FreePagesBefore)

	d := m.Delete
	vs.check(size, boundDeleteOwnsRows, d.OwnedRows > 0, "the deleted project owned no rows")
	vs.check(size, boundDeleteRows, d.Work["rows_changed"] <= deleteRowsPerOwnedRow*d.OwnedRows,
		"delete changed %d rows for %d owned rows", d.Work["rows_changed"], d.OwnedRows)
	for _, peak := range []string{"peak_statement_rows_changed", "peak_transaction_rows_changed", "peak_statement_rows_returned"} {
		vs.check(size, boundDeleteStepRows, d.Work[peak] <= deleteStepRows, "delete %s is %d, above %d", peak, d.Work[peak], deleteStepRows)
	}

	u := m.Upgrade
	upgradeLimit := upgradeBasePages + upgradePagesPerStorePage*u.StorePages
	vs.check(size, boundUpgradePages, u.Work["pages_fetched"] <= upgradeLimit,
		"upgrade fetched %d pages from a store of %d, above %d", u.Work["pages_fetched"], u.StorePages, upgradeLimit)
	vs.check(size, boundUpgradeStepRows, u.Work["peak_statement_rows_returned"] <= upgradeStepRows,
		"upgrade peak_statement_rows_returned is %d, above %d", u.Work["peak_statement_rows_returned"], upgradeStepRows)
	vs.check(size, boundUpgradeMeasured, u.Work["peak_resident_bytes"] > 0, "upgrade peak resident memory was not measured")
}

func perUnit(count, units int64) float64 {
	if units <= 0 {
		return math.Inf(1)
	}
	return float64(count) / float64(units)
}

func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
