package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route with reviewed sorter inputs.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "16a84cf618f8a79c463a703418068a284f9455e8d14d9b90d1f634bc5da5f6d5"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// Manifest inserts maintain their indexes incrementally. Receipt index
	// rebuilds use the measured receipt tree. The evidence, file edit,
	// worker record, and source file request rewrites use their declared
	// write estimates. Briefing and publication indexes start empty.
	// Dropping live_model_outputs and replacing the version reclaim trigger
	// do not rewrite rows. Workflow manifest storage adds an empty table
	// and a constant-default column.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "975a289c2ead3db716cea465c7c185ca9754e12dafb7dc1e5c73f7ec34f9fa1a"
}
