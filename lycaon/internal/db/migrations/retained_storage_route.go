package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route on the reviewed SQLite build.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	target := Baseline{Revision: 5, Shape: "b3983b88fb550e59b1de12ce4280b09654eb0abd9700b744c0d7df8d028c4b58"}
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Source != manifestLayoutSource ||
		p.Target != target || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	step := p.steps[0]
	return step.From == p.Source && step.To == p.Target &&
		step.ID == "source-manifests-and-command-records" &&
		step.Checksum == "bcc51827a02450adf80af1b93604236453e63b58b2e5da7c179a25dbbf503e30"
}
