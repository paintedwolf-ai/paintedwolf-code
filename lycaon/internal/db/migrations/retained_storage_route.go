package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route on the reviewed SQLite build.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Source != manifestLayoutSource || p.CopyPlan() == nil || len(p.steps) != 2 {
		return false
	}
	first, second := p.steps[0], p.steps[1]
	return first.From == p.Source && first.To == releasedRevision5 &&
		first.ID == revision5ID && first.Checksum == revision5Checksum &&
		second.From == releasedRevision5 && second.To == p.Target && p.Target.Revision == 6 &&
		second.ID == revision6ID && second.Checksum == revision6Checksum
}
