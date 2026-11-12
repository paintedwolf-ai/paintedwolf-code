package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Released jobs become workers: one per child session, one per job without a
// child session, named from the agent. A later job on the same child settles
// the earlier open round; only receipted landings become dispositions; held
// prerequisites carry over; jobs of deleted sessions get no worker.
func TestReleasedJobsBecomeWorkers(t *testing.T) {
	original, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.1", "store.db"))
	testutil.FailErr(t, "read released corpus", err)
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released corpus", os.WriteFile(path, original, 0600))
	src, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released store", err)
	var project, owner string
	testutil.FailErr(t, "read project", src.QueryRowContext(t.Context(), `SELECT id FROM projects LIMIT 1`).Scan(&project))
	testutil.FailErr(t, "read owner", src.QueryRowContext(t.Context(), `SELECT id FROM people LIMIT 1`).Scan(&owner))
	const (
		t1 = "2026-10-01T10:00:00Z"
		t2 = "2026-10-01T10:05:00Z"
		t3 = "2026-10-01T10:09:00Z"
	)
	exec := func(label, query string, args ...any) {
		t.Helper()
		_, err := src.ExecContext(t.Context(), query, args...)
		testutil.FailErr(t, label, err)
	}
	session := func(id string, parent any) {
		exec("insert session "+id, `INSERT INTO sessions (id, project_id, owner_person_id, posture, parent_session_id, created_at, activity_at, updated_at)
			VALUES (?, ?, ?, 'build', ?, ?, ?, ?)`, id, project, owner, parent, t1, t1, t1)
	}
	job := func(id string, parent, child any, agent, status, created string, completed any, merge any) {
		exec("insert job "+id, `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, scope_json, created_at, completed_at, merge_status)
			VALUES (?, ?, ?, ?, ?, ?, 'prompt', 'brief of '||?, '{"mode":"write"}', ?, ?, ?)`, id, project, parent, child, agent, status, id, created, completed, merge)
	}
	session("parent", nil)
	session("other-parent", nil)
	session("child-1", "parent")
	session("foreign-child", "other-parent")
	job("j1", "parent", "child-1", "implementer", "complete", t1, t1, "merged")
	exec("insert result", `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES ('r1', 'j1', 'complete', ?)`, t1)
	exec("insert landed", `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES ('l1', 'j1', '/repo', ?)`, t1)
	job("j2", "parent", "child-1", "implementer", "running", t2, nil, nil)
	job("j3", "parent", "child-1", "implementer", "pending", t3, nil, nil)
	job("j4", "parent", nil, "implementer", "held", t3, nil, nil)
	exec("insert prerequisite", `INSERT INTO worker_prerequisites (worker_job_id, prerequisite_id) VALUES ('j4', 'j1')`)
	job("j5", "parent", "foreign-child", "repo-researcher", "complete", t2, t2, nil)
	job("j6", nil, nil, "implementer", "complete", t1, t1, nil)
	testutil.FailErr(t, "close released store", src.Close())

	testutil.FailErr(t, "upgrade", UpgradeStaged(t.Context(), path))
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open upgraded store", err)
	defer reader.Close()

	workers := map[string]string{}
	rows, err := reader.QueryContext(t.Context(), `SELECT name, id, COALESCE(child_session_id, ''), scope_mode, brief FROM workers WHERE session_id = 'parent' ORDER BY name`)
	testutil.FailErr(t, "read workers", err)
	for rows.Next() {
		var name, id, child, scope, brief string
		testutil.FailErr(t, "scan worker", rows.Scan(&name, &id, &child, &scope, &brief))
		workers[name] = id
		switch name {
		case "implementer-1":
			if child != "child-1" || scope != "write" || brief != "brief of j3" {
				t.Fatalf("implementer-1 = child %q scope %q brief %q", child, scope, brief)
			}
		case "repo-researcher-1":
			if child != "" {
				t.Fatalf("a child of another session was bound: %q", child)
			}
		}
	}
	testutil.FailErr(t, "close workers", rows.Close())
	if len(workers) != 3 || workers["implementer-1"] == "" || workers["implementer-2"] == "" || workers["repo-researcher-1"] == "" {
		t.Fatalf("workers = %v", workers)
	}

	type round struct {
		ordinal int
		job     string
		settled sql.NullString
	}
	var rounds []round
	rows, err = reader.QueryContext(t.Context(), `SELECT ordinal, job_id, settled_at FROM worker_rounds WHERE worker_id = ? ORDER BY ordinal`, workers["implementer-1"])
	testutil.FailErr(t, "read rounds", err)
	for rows.Next() {
		var r round
		testutil.FailErr(t, "scan round", rows.Scan(&r.ordinal, &r.job, &r.settled))
		rounds = append(rounds, r)
	}
	testutil.FailErr(t, "close rounds", rows.Close())
	if len(rounds) != 3 || rounds[0].job != "j1" || rounds[0].settled.String != t1 ||
		rounds[1].job != "j2" || rounds[1].settled.String != t3 || rounds[2].job != "j3" || rounds[2].settled.Valid {
		t.Fatalf("rounds = %+v", rounds)
	}

	var state, landed string
	var accounted sql.NullString
	testutil.FailErr(t, "read disposition", reader.QueryRowContext(t.Context(),
		`SELECT change_state, COALESCE(landed_change_id, ''), accounted_at FROM worker_result_dispositions WHERE result_id = 'r1'`).Scan(&state, &landed, &accounted))
	if state != "landed" || landed != "l1" || accounted.Valid {
		t.Fatalf("r1 disposition = %s %s accounted %v", state, landed, accounted)
	}
	var dispositions, producers, orphanRounds, receipts int
	testutil.FailErr(t, "count dispositions", reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_result_dispositions`).Scan(&dispositions))
	testutil.FailErr(t, "count dependencies", reader.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM worker_dependencies WHERE worker_id = ? AND producer_worker_id = ?`, workers["implementer-2"], workers["implementer-1"]).Scan(&producers))
	testutil.FailErr(t, "count orphan rounds", reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_rounds WHERE job_id = 'j6'`).Scan(&orphanRounds))
	testutil.FailErr(t, "count receipts", reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_operations`).Scan(&receipts))
	if dispositions != 1 || producers != 1 || orphanRounds != 0 || receipts != 0 {
		t.Fatalf("dispositions %d, prerequisites %d, orphan rounds %d, receipts %d", dispositions, producers, orphanRounds, receipts)
	}

	// The corpus's own completed job became a worker with one settled round.
	var released int
	testutil.FailErr(t, "count released worker", reader.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM worker_rounds r JOIN workers w ON w.id = r.worker_id
		 WHERE r.job_id = '5e9f4f43-19b5-462a-98c1-2296f013dc94' AND r.settled_at IS NOT NULL
		   AND w.child_session_id = '06ed0263-4b18-4402-8ad3-4c924f8ff354'`).Scan(&released))
	if released != 1 {
		t.Fatalf("released corpus job rounds = %d", released)
	}
	testutil.FailErr(t, "upgrade again", UpgradeStaged(t.Context(), path))
}
