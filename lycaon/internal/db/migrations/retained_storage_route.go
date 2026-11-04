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
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "3a065b2adccf2628ce0e203a4e04962c974b462a7cae85e8996ba4e7696fe9e6"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "3d9ef50b62e8914d35715d79b4cc5f4723c48b30b534fc6e230f870d44721f51"
}
