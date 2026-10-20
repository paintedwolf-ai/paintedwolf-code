package db

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEffectPermitUpgradeKnownIntermediateAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source, err := Open(path)
	testutil.FailErr(t, "open isolated source fixture", err)
	_, err = source.ExecContext(t.Context(), `INSERT INTO projects(id,last_opened_at,created_at) VALUES('fixture-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed project", err)
	_, err = source.ExecContext(t.Context(), `INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('fixture-session','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed actual session scope", err)
	const retained = `{"approval_plan":{"id":"plan","action_digest":"semantic","stage":"pre_dial","recommended_option_id":"once","options":[{"id":"once","authority":[{"kind":"direct_ip_permit","session_id":"fixture-session","tool_call_id":"actual-model-call","action_digest":"semantic","direct_ip_lease":{"ActionDigest":"semantic","RequestDigest":"requested","ConfinementDigest":"confined","ChatConfinementDigest":"","DeclaredDestinations":["fixture.invalid"],"CommandSummary":"fixture"}}]}]}}`
	_, err = source.ExecContext(t.Context(), `INSERT INTO checkpoints(id,session_id,project_id,kind,status,created_at,payload_json) VALUES('fixture-checkpoint','fixture-session','fixture-project','tool_approval','pending','2026-01-01T00:00:00Z',?)`, retained)
	testutil.FailErr(t, "seed retained once metadata", err)
	_, err = source.ExecContext(t.Context(), `PRAGMA user_version=4`)
	testutil.FailErr(t, "claim exact known intermediate", err)
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect known source", err)
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect fresh target", err)
	if actual != migrations.EffectPermitIdentity(target).From {
		t.Fatalf("fixture differs from registered source: %+v", actual)
	}
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade isolated source", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "idempotent restart", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "read upgraded store", err)
	defer upgraded.Close()
	got, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect target", err)
	if got != target {
		t.Fatalf("wrong target: %+v", got)
	}
	var raw string
	var count int
	testutil.FailErr(t, "read migrated checkpoint", upgraded.QueryRow(`SELECT payload_json FROM checkpoints WHERE id='fixture-checkpoint'`).Scan(&raw))
	var payload struct {
		Plan struct {
			Options []struct {
				Authority []struct {
					ExecutionID  *string `json:"execution_id"`
					ToolCallID   string  `json:"tool_call_id"`
					ActionDigest string  `json:"action_digest"`
				} `json:"authority"`
			} `json:"options"`
		} `json:"approval_plan"`
	}
	testutil.FailErr(t, "decode migrated checkpoint", json.Unmarshal([]byte(raw), &payload))
	delta := payload.Plan.Options[0].Authority[0]
	if delta.ExecutionID == nil || *delta.ExecutionID != "" || delta.ToolCallID != "actual-model-call" || delta.ActionDigest != "semantic" {
		t.Fatal("retained authority acquired native identity or changed model meaning")
	}
	testutil.FailErr(t, "read migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='native-child-once-authority'`).Scan(&count))
	if count != 1 {
		t.Fatalf("migration applied %d times", count)
	}
}
