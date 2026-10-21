package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

// DeriveRevision4Fixture derives the registered revision4 shape: revision5
// without the native authority columns it adds.
func DeriveRevision4Fixture(t *testing.T, source DBTX) {
	t.Helper()
	DeriveRevision5Fixture(t, source)
	for _, table := range []string{"checkpoints", "approval_operations", "chat_grants"} {
		for _, column := range []string{"authority_body", "authority_digest", "authority_format"} {
			_, err := source.ExecContext(t.Context(), "ALTER TABLE "+table+" DROP COLUMN "+column)
			testutil.FailErr(t, "remove revision5 column", err)
		}
	}
	_, err := source.ExecContext(t.Context(), `PRAGMA user_version=4`)
	testutil.FailErr(t, "claim revision4", err)
}

func revision4Store(t *testing.T, path string) *Store {
	t.Helper()
	source, err := Open(path)
	testutil.FailErr(t, "open isolated source fixture", err)
	DeriveRevision4Fixture(t, source)
	return source
}

func TestEffectPermitUpgradeKnownIntermediateAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := revision4Store(t, path)
	var err error
	_, err = source.ExecContext(t.Context(), `INSERT INTO projects(id,last_opened_at,created_at) VALUES('fixture-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed project", err)
	_, err = source.ExecContext(t.Context(), `INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('fixture-session','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed actual session scope", err)
	const retained = `{"approval_plan":{"id":"plan","action_digest":"semantic","stage":"pre_dial","recommended_option_id":"once","options":[{"id":"once","authority":[{"kind":"direct_ip_permit","session_id":"fixture-session","tool_call_id":"actual-model-call","action_digest":"semantic","direct_ip_lease":{"ActionDigest":"semantic","RequestDigest":"requested","ConfinementDigest":"confined","ChatConfinementDigest":"","DeclaredDestinations":["fixture.invalid"],"CommandSummary":"fixture"}}]}]}}`
	_, err = source.ExecContext(t.Context(), `INSERT INTO checkpoints(id,session_id,project_id,kind,status,created_at,payload_json) VALUES('fixture-checkpoint','fixture-session','fixture-project','tool_approval','pending','2026-01-01T00:00:00Z',?)`, retained)
	testutil.FailErr(t, "seed retained once metadata", err)
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
	var format, digest, body string
	testutil.FailErr(t, "read retained authority columns", upgraded.QueryRow(`SELECT authority_format,authority_digest,authority_body FROM checkpoints WHERE id='fixture-checkpoint'`).Scan(&format, &digest, &body))
	if format != "" || digest != "" || body != "" {
		t.Fatal("retained structured authority acquired a native format")
	}
}

// A store claiming revision5 without its native authority columns is an
// unknown shape: planning refuses it and leaves its bytes untouched.
func TestNativeAuthorityColumnlessRevision5IsRefusedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := revision4Store(t, path)
	_, err := source.ExecContext(t.Context(), `PRAGMA user_version=5`)
	testutil.FailErr(t, "claim revision5", err)
	testutil.FailErr(t, "close source", source.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read source bytes", err)
	var incompatible *StoreIncompatibleError
	if err := UpgradeStaged(t.Context(), path); !errors.As(err, &incompatible) || incompatible.Reason != RecoveryReasonSchemaMismatch || incompatible.StoreSchemaVersion != 5 {
		t.Fatalf("columnless revision5 was not refused as a schema mismatch: %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused bytes", err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused store was mutated")
	}
	if _, err := PlanSchemaUpgrade(t.Context(), migrations.Baseline{Revision: 5, Shape: migrations.EffectPermitIdentity(migrations.Baseline{}).From.Shape}); !errors.Is(err, migrations.ErrUnsupported) {
		t.Fatalf("columnless revision5 identity planned: %v", err)
	}
}
