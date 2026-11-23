package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route with reviewed sorter inputs.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "1f17192a3ea624d2a0f89749252d3f0698acd0a5aaa5c12185f0cf47fe8b6ad3"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// Manifest inserts maintain their indexes incrementally. Receipt index
	// rebuilds use the measured receipt tree. The evidence, file edit, and
	// worker record rewrites use their declared write estimates. Briefing
	// and publication indexes start empty. Dropping live_model_outputs and
	// replacing the version reclaim trigger do not rewrite rows. Workflow
	// manifest storage adds an empty table and a constant-default column.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "aba36a003f99d08716affd4bea9d95366b425678e593b071f62e0641bcf5f349"
}
