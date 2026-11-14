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
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 5, Shape: "b833c6fa257cef3ef298bf394b2ce4069206f724d870403c65f10fc2c11beec5"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	// The manifest writer bounds its re-derivable memos and maintains target
	// indexes during insertion. Receipt index rebuilds use the measured receipt
	// tree. The evidence root backfill and single-to-plural file edit rewrite
	// bound their row rewrites and index sync through declared Writes estimates,
	// adding no sorters or rebuilds.
	return p.steps[0].ID == "source-manifests-and-command-records" &&
		p.steps[0].Checksum == "c5c2979a6e4b34c0e52a5538c5171c01fc022ddd48f6c13b3d605254abe8744f"
}
