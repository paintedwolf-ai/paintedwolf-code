package main

import (
	"fmt"
	"math"

	"github.com/lycaon/lycaon/internal/db"
)

// Declared work bounds. Every bound is a count; none is a duration.
const (
	// Boot does the same work on a small store and a large one, within these spreads.
	bootPagesSpread      = 2_048
	bootStatementsSpread = 64
	bootRowsSpread       = 1_024
	// Boot opens no file in the project tree.
	bootFilesOpened = 0

	// Deleting a project changes at most this many rows per row it owns,
	// counting the rows triggers change.
	deleteRowsPerOwnedRow = 4
	// No statement or transaction of a delete changes or returns more rows.
	deleteStepRows = 16_384

	// An upgrade fetches at most this many pages per page in the store.
	upgradePagesPerStorePage = 8
	// No upgrade statement returns more rows.
	upgradeStepRows = 4_096
	// An upgrade's peak resident memory differs between sizes by at most this.
	upgradeResidentSpread = 64 << 20

	// Work per unit at the large size is at most this multiple of the small
	// size's, which a quadratic factor exceeds by ten times the size ratio.
	scaleGrowth = 2.0
)

func scaleBounds() map[string]float64 {
	return map[string]float64{
		"boot_pages_spread": bootPagesSpread, "boot_statements_spread": bootStatementsSpread,
		"boot_rows_spread": bootRowsSpread, "boot_files_opened": bootFilesOpened,
		"delete_rows_per_owned_row": deleteRowsPerOwnedRow, "delete_step_rows": deleteStepRows,
		"upgrade_pages_per_store_page": upgradePagesPerStorePage, "upgrade_step_rows": upgradeStepRows,
		"upgrade_resident_spread": upgradeResidentSpread, "vacuum_pass_pages": db.VacuumPassPageLimit,
		"growth": scaleGrowth,
	}
}

// evaluateScale checks every bound on one size, then the spreads and growth
// between the small and the large size.
func evaluateScale(small, large scaleMeasurement) []string {
	violations := append(evaluateSize(small), evaluateSize(large)...)
	check := func(ok bool, format string, args ...any) {
		if !ok {
			violations = append(violations, fmt.Sprintf(format, args...))
		}
	}
	for _, spread := range []struct {
		counter string
		limit   int64
	}{{"pages_fetched", bootPagesSpread}, {"statements", bootStatementsSpread}, {"rows_returned", bootRowsSpread}} {
		difference := large.Boot.Work[spread.counter] - small.Boot.Work[spread.counter]
		check(difference <= spread.limit, "boot %s grew by %d between sizes, above %d", spread.counter, difference, spread.limit)
	}
	for _, counter := range []string{"statements", "pages_fetched", "rows_changed"} {
		smallRate := perUnit(small.Delete.Work[counter], small.Delete.OwnedRows)
		largeRate := perUnit(large.Delete.Work[counter], large.Delete.OwnedRows)
		check(largeRate <= smallRate*scaleGrowth, "delete %s per owned row grew from %.3f to %.3f between sizes", counter, smallRate, largeRate)
	}
	smallRate := perUnit(small.Upgrade.Work["pages_fetched"], small.Upgrade.StorePages)
	largeRate := perUnit(large.Upgrade.Work["pages_fetched"], large.Upgrade.StorePages)
	check(largeRate <= smallRate*scaleGrowth, "upgrade pages fetched per store page grew from %.3f to %.3f between sizes", smallRate, largeRate)
	resident := large.Upgrade.Work["peak_resident_bytes"] - small.Upgrade.Work["peak_resident_bytes"]
	check(resident <= upgradeResidentSpread, "upgrade peak resident memory grew by %d bytes between sizes, above %d", resident, upgradeResidentSpread)
	return violations
}

func evaluateSize(m scaleMeasurement) []string {
	var violations []string
	check := func(ok bool, format string, args ...any) {
		if !ok {
			violations = append(violations, m.Size.Name+": "+fmt.Sprintf(format, args...))
		}
	}
	check(m.Boot.FilesOpened >= 0 && m.Boot.FilesOpened <= bootFilesOpened, "boot opened %d project files, above %d", m.Boot.FilesOpened, bootFilesOpened)

	v := m.Vacuum
	check(v.PeakPassPages <= db.VacuumPassPageLimit, "a vacuum pass freed %d pages, above %d", v.PeakPassPages, db.VacuumPassPageLimit)
	passLimit := ceilDiv(v.FreePagesBefore, v.PassPages) + 1
	check(v.PassPages > 0 && v.Passes <= passLimit, "vacuum took %d passes for %d free pages, above %d", v.Passes, v.FreePagesBefore, passLimit)
	check(v.Turns >= v.Passes, "vacuum ran %d passes in %d turns; every pass waits for its own turn", v.Passes, v.Turns)
	check(v.PagesReclaimed > 0 && v.FreePagesAfter < v.FreePagesBefore, "vacuum reclaimed %d pages, leaving %d of %d free", v.PagesReclaimed, v.FreePagesAfter, v.FreePagesBefore)

	d := m.Delete
	check(d.OwnedRows > 0, "the deleted project owned no rows")
	check(d.Work["rows_changed"] <= deleteRowsPerOwnedRow*d.OwnedRows, "delete changed %d rows for %d owned rows, above %d per row", d.Work["rows_changed"], d.OwnedRows, deleteRowsPerOwnedRow)
	for _, peak := range []string{"peak_statement_rows_changed", "peak_transaction_rows_changed", "peak_statement_rows_returned"} {
		check(d.Work[peak] <= deleteStepRows, "delete %s is %d, above %d", peak, d.Work[peak], deleteStepRows)
	}

	u := m.Upgrade
	check(u.Work["pages_fetched"] <= upgradePagesPerStorePage*u.StorePages, "upgrade fetched %d pages from a store of %d, above %d per page", u.Work["pages_fetched"], u.StorePages, upgradePagesPerStorePage)
	check(u.Work["peak_statement_rows_returned"] <= upgradeStepRows, "upgrade peak_statement_rows_returned is %d, above %d", u.Work["peak_statement_rows_returned"], upgradeStepRows)
	check(u.Work["peak_resident_bytes"] > 0, "upgrade peak resident memory was not measured")
	return violations
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
