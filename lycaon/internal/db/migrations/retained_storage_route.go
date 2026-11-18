package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage recognizes exactly the route whose indexes and
// sorter inputs were reviewed. A future step keeps conservative admission
// until its physical work has been included in that proof.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "cc232c84e13f467f6cc28c9a04bc8c2b2f6bd36814a36f619bd757dd45e54863"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// The manifest writer bounds its re-derivable memos and maintains target
	// indexes during insertion. Receipt index rebuilds use the measured receipt
	// tree. New receipt columns are on an empty table. The evidence root backfill,
	// single-to-plural file edit rewrite,
	// empty briefing tables, and worker records backfill bound their row
	// rewrites and index sync through declared Writes estimates, adding no
	// sorters or rebuilds. Discarding live_model_outputs drops a table without
	// rewriting rows.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "db5fb841d03f01918ebfb4750f2f7544c6166def600a81c5e80eb203695c14f4"
}
