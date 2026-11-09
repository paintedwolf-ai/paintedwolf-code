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
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "44dc099d7ed120af6694718365f1c24d42f5f0ccfbec0cf5690eb3d30f8f725b"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// The manifest writer bounds its re-derivable memos and maintains target
	// indexes during insertion, preserving the source-sized sorter proof. The
	// evidence root backfill bounds its record rewrites and index sync through
	// its declared Writes estimate, adding no sorters or rebuilds.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "7d697e2aab0a567b69fbad432665278f38943ede3c4bb79212c1da55637a4a89"
}
