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
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "4572ca290ba86b424ecbb400f5ad28c5a44d2cb793cbfaf8d75c11585879b303"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// The manifest writer bounds its re-derivable memos and maintains target
	// indexes during insertion. Receipt index rebuilds use the measured receipt tree.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "bd7d9539e0c246a3f44126af219fffa6925a74b3783a01a9cfe20f806d2fb203"
}
