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
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "71741e5f8d9a6ead298d061e3726d0b045b32ca62570409144eb52a2740151f5"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// The manifest writer bounds its re-derivable memos and maintains target
	// indexes during insertion, preserving the source-sized sorter proof.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "cb788e40a6e86625f2e9ec812cbcc54943c19e085791cdd05c285cb4eed4b30d"
}
