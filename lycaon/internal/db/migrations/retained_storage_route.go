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
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 6, Shape: "34f505e9c4c29dc855ac5dd78e48a4015914563f7cb3d053bea906ebf6baddb2"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 2 {
		return false
	}
	// The manifest writer bounds its re-derivable memos and maintains target
	// indexes during insertion. Receipt index rebuilds use the measured receipt
	// tree. The evidence root backfill, single-to-plural file edit rewrite,
	// empty briefing tables, and worker records backfill bound their row
	// rewrites and index sync through declared Writes estimates, adding no
	// sorters or rebuilds. Discarding live_model_outputs drops a table without
	// rewriting rows.
	// Workflow storage adds an empty table and a metadata-only column.
	return p.steps[1].ID == "workflow-manifest-storage" && p.steps[1].Checksum == "37a3dfc897a09d0bdc94a5d25829ed8b1d1630658fc47e419add561a3e370da6" && p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "8e1afb6494bcc70516ccbb226a0128d9d2d566bac1d05a829ed98aa7daeb93ae"
}
