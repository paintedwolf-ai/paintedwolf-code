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
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Source != manifestLayoutSource || p.CopyPlan() == nil {
		return false
	}
	switch len(p.steps) {
	case 1:
		return p.Target == revision5Release && reviewedRevision5(p.steps[0])
	case 2:
		return p.Target == revision6Target && reviewedRevision5(p.steps[0]) && reviewedRevision6(p.steps[1])
	}
	return false
}

// revision6Target is the shape the reviewed revision-6 step produces.
var revision6Target = Baseline{Revision: 6, Shape: "1be9f1c48f33fdca8c9ffb417993c5fff801aa683ddece54f69ef5fd95708c20"}

// The manifest writer bounds its re-derivable memos and maintains target
// indexes during insertion. Receipt index rebuilds use the measured receipt
// tree. The evidence root backfill, single-to-plural file edit rewrite, and
// empty briefing tables bound their row rewrites and index sync through
// declared Writes estimates, adding no sorters or rebuilds.
func reviewedRevision5(step Step) bool {
	return step.ID == "source-manifests-and-command-records" &&
		step.Checksum == "06665671cc951c7dac37e1e766361205999b0f3f83aea03cad3eb8bec38e04db"
}

// The outside namespace rebuilds project_roots, whose rows are the projects'
// roots plus one per project, and creates one empty table.
func reviewedRevision6(step Step) bool {
	return step.ID == "outside-namespace" &&
		step.Checksum == "f5e1e557853468d71dedcbf8be694b4da913af5f8d2ce85185a4b30034cd1aa5"
}
