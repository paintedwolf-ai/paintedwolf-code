package migrations

import sqlite3 "modernc.org/sqlite/lib"

var reviewedRetainedTarget = Baseline{Revision: 5, Shape: "fe24b55031e83df3f0a868536623a6298f2419950fb40aac72b2a87ed80ae433"}

// BoundedRetainedStorage recognizes exactly the route whose indexes and
// sorter inputs were reviewed. A future step keeps conservative admission
// until its physical work has been included in that proof.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != reviewedRetainedTarget {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// The manifest writer bounds its re-derivable memos and maintains target
	// indexes during insertion. Receipt index rebuilds use the measured receipt
	// tree. The evidence root backfill, single-to-plural file edit rewrite,
	// empty briefing tables, and worker records backfill bound their row
	// rewrites and index sync through declared Writes estimates, adding no
	// sorters or rebuilds. Discarding live_model_outputs drops a table without
	// rewriting rows. The outside namespace rebuilds the measured root indexes
	// and inserts one row per project. Chat locations and observation coverage
	// add empty tables. Existing editor rows use the binding column's default.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "b277cbcfdb3b3c0cb5fe291b8ce2a4482f4d7dfd243a2f39cb898b0be2fcc4b6"
}
