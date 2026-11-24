package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route with reviewed sorter inputs.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "616ca6576c84b4d055b6eb519b1748977b93b8bb4664ab30658141c6d4aa5f14"}) {
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
	// manifest storage adds an empty table and a constant-default column, and
	// Home starts add empty tables.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "5aa3616cf7de48fa19a0f745bf232954577dd8a7c6ac42c19f87369c259b0811"
}
