package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route with reviewed sorter inputs.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Source != manifestLayoutSource || p.CopyPlan() == nil {
		return false
	}
	// Home starts then add two empty tables, their indexes and two triggers.
	switch {
	case len(p.steps) == 1 && p.Target == Revision5Baseline:
	case len(p.steps) == 2 && p.steps[1].ID == "home-starts" && p.steps[1].Checksum == homeStartsChecksum:
	default:
		return false
	}
	// Manifest inserts maintain their indexes incrementally. Receipt index
	// rebuilds use the measured receipt tree. The evidence, file edit, and
	// worker record rewrites use their declared write estimates. Briefing
	// and publication indexes start empty. Dropping live_model_outputs and
	// replacing the version reclaim trigger do not rewrite rows. Workflow
	// manifest storage adds an empty table and a constant-default column.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "02cc4ded97994fdd6c993f9ec200dc5875b72811f44496dc5d3a5638a635672b"
}
