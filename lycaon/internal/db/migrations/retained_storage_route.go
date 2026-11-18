package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route with reviewed sorter inputs.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "86a28628978a597da3b09952a94bcd20493396b7babb4500e9dfbc1509c7f066"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// Manifest inserts maintain their indexes incrementally. Receipt index
	// rebuilds use the measured receipt tree. The evidence, file edit, and
	// worker record rewrites use their declared write estimates. Briefing
	// and publication indexes start empty. Dropping live_model_outputs
	// does not rewrite rows.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "d2a5deb29cffcab14d32cc9c36ac050695fd7fcda024d2cd03e07988bdaf9aae"
}
