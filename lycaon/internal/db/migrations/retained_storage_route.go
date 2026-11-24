package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route with reviewed sorter inputs.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 6, Shape: "07cc74938f41fb3fe50631365c934282818fd83918131196aa6932633c8451e9"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 2 {
		return false
	}
	// Manifest inserts maintain their indexes incrementally. Receipt index
	// rebuilds use the measured receipt tree. The evidence, file edit, and
	// worker record rewrites use their declared write estimates. Briefing
	// and publication indexes start empty. Dropping live_model_outputs and
	// replacing the version reclaim trigger do not rewrite rows. Workflow
	// manifest storage adds an empty table and a constant-default column.
	// Revision 6 adds an empty coordinator completion-cue table.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "02cc4ded97994fdd6c993f9ec200dc5875b72811f44496dc5d3a5638a635672b" &&
		p.steps[1].ID == "coordinator-completion-cues" &&
		p.steps[1].Checksum == "873ad25e6279cd188d3a8c8971cfa7cbc40d94979cd1bdaa03ac4a658cae9a06"
}
