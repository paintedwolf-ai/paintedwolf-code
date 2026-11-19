package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage requires a measured route on the reviewed SQLite build.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	target := Baseline{Revision: 5, Shape: "b5e7468adb7ecc99d5e86cd22a057cef77a4f1de94e14bbb2100eccb40b485d2"}
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Source != manifestLayoutSource ||
		p.Target != target || p.CopyPlan() == nil || len(p.steps) != 1 {
		return false
	}
	step := p.steps[0]
	return step.From == p.Source && step.To == p.Target &&
		step.ID == "source-manifests-and-command-records" &&
		step.Checksum == "c89ca3f37b41d2ff9efc47edd7365dd2bb8649d2baf084109192eb7b070673a2"
}
