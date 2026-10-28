package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramReceiptsUpgradeKnownIntermediateAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := authorityRevision7Store(t, path)
	var err error
	_, err = source.ExecContext(t.Context(), `INSERT INTO projects(id,last_opened_at,created_at) VALUES('fixture-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'); INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('fixture-session','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`)
	testutil.FailErr(t, "seed retained actual session", err)
	retained := seedNativeAuthorityMainData(t, source)
	_, err = source.ExecContext(t.Context(), `INSERT INTO invocation_receipts(id,project_id,session_id,tool_call_id,tool_name,owner,lifecycle,reversibility,evidence_policy,recovery_policy,evidence_kind,contract_digest,args_digest,status,started_at) VALUES('fixture-parent','fixture-project','fixture-session','actual-model-call','command','processes','effect_attempt','irreversible','attempt','abandon','pending','contract','args','running','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed real parent receipt", err)
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target", err)
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect known intermediate", err)
	if actual != migrations.ProgramReceipts(target).From {
		t.Fatalf("wrong intermediate: %+v", actual)
	}
	testutil.FailErr(t, "close isolated source", source.Close())
	testutil.FailErr(t, "upgrade actual registered route", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "restart without repeating migration", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "inspect upgraded database", err)
	defer upgraded.Close()
	actual, err = inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded shape", err)
	if actual != target {
		t.Fatalf("wrong upgraded target: %+v", actual)
	}
	for table, before := range retained {
		if got := namespaceTableContents(t, upgraded, table); got != before {
			t.Fatalf("receipt migration changed retained %s data", table)
		}
	}
	var modelCall string
	testutil.FailErr(t, "preserve actual parent identity", upgraded.QueryRow(`SELECT tool_call_id FROM invocation_receipts WHERE id='fixture-parent'`).Scan(&modelCall))
	if modelCall != "actual-model-call" {
		t.Fatal("parent model call changed")
	}
	var count int
	testutil.FailErr(t, "inspect migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='portable-program-receipts'`).Scan(&count))
	if count != 1 {
		t.Fatalf("migration applied %d times", count)
	}
	testutil.FailErr(t, "no fabricated children", upgraded.QueryRow(`SELECT count(*) FROM invocation_program_executions`).Scan(&count))
	if count != 0 {
		t.Fatal("migration fabricated host executions")
	}
}
