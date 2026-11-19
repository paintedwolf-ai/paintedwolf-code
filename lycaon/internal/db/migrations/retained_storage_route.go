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
	// The storage bound applies to these reviewed migration statements.
	first, second := p.steps[0], p.steps[1]
	return first.From == p.Source && first.To == releasedRevision5 &&
		first.ID == "source-manifests-and-command-records" &&
		first.Checksum == "8e1afb6494bcc70516ccbb226a0128d9d2d566bac1d05a829ed98aa7daeb93ae" &&
		second.From == releasedRevision5 && second.To == p.Target && p.Target.Revision == 6 &&
		second.ID == "session-mode-authority" &&
		second.Checksum == "77168ac8ec2e74b3a9408abffc7b7bed8bbd39e918a51f47c3f938c100b693b2"
}
