package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	_ "modernc.org/sqlite"
)

const retainedOnceFixture = `{"annotation":"retained","approval_plan":{"id":"plan","action_digest":"semantic-digest","stage":"pre_dial","recommended_option_id":"once","options":[{"id":"once","authority":[{"kind":"socket_permit","session_id":"session","tool_call_id":"actual-call","action_digest":"semantic-digest","sockets":[{"approved_path":"/tmp/fixture.sock","resolved_path":"/tmp/fixture.sock"}]}]}]}}`

func TestEffectPermitMigrationPreservesStructuredIdentity(t *testing.T) {
	got, err := structuredEffectPermitPayload([]byte(retainedOnceFixture))
	testutil.FailErr(t, "migrate structured once identity", err)
	if !bytes.Contains(got, []byte(`"execution_id":""`)) {
		t.Fatalf("missing explicit structured scope: %s", got)
	}
	var before, after map[string]any
	testutil.FailErr(t, "decode original", json.Unmarshal([]byte(retainedOnceFixture), &before))
	testutil.FailErr(t, "decode migrated", json.Unmarshal(got, &after))
	delta := after["approval_plan"].(map[string]any)["options"].([]any)[0].(map[string]any)["authority"].([]any)[0].(map[string]any)
	delete(delta, "execution_id")
	normalized, err := json.Marshal(after)
	testutil.FailErr(t, "encode migrated", err)
	original, err := json.Marshal(before)
	testutil.FailErr(t, "encode original", err)
	if !bytes.Equal(original, normalized) {
		t.Fatal("migration changed plan, semantic grant or model-call identity")
	}
	unchanged, err := structuredEffectPermitPayload([]byte(`{"other":"retained"}`))
	testutil.FailErr(t, "retained unrelated payload", err)
	if unchanged != nil {
		t.Fatal("unrelated payload rewritten")
	}
}

func TestEffectPermitMigrationRefusesUnknownAuthorityShapes(t *testing.T) {
	for name, input := range map[string]string{
		"unknown kind":       strings.Replace(retainedOnceFixture, `"socket_permit"`, `"future_permit"`, 1),
		"unknown field":      strings.Replace(retainedOnceFixture, `"kind":"socket_permit"`, `"kind":"socket_permit","future_grant":true`, 1),
		"native identity":    strings.Replace(retainedOnceFixture, `"kind":"socket_permit"`, `"kind":"socket_permit","execution_id":"child"`, 1),
		"empty new identity": strings.Replace(retainedOnceFixture, `"kind":"socket_permit"`, `"kind":"socket_permit","execution_id":""`, 1),
		"missing parent":     strings.Replace(retainedOnceFixture, `"tool_call_id":"actual-call",`, "", 1),
		"empty options":      `{"approval_plan":{"id":"plan","action_digest":"digest","stage":"pre_dial","recommended_option_id":"once","options":[]}}`,
		"unknown socket":     strings.Replace(retainedOnceFixture, `"approved_path":"/tmp/fixture.sock"`, `"approved_path":"/tmp/fixture.sock","future_path":"/tmp/other.sock"`, 1),
		"wrong socket type":  strings.Replace(retainedOnceFixture, `"approved_path":"/tmp/fixture.sock"`, `"approved_path":42`, 1),
		"unknown plan":       strings.Replace(retainedOnceFixture, `"id":"plan"`, `"id":"plan","future_release":true`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := structuredEffectPermitPayload([]byte(input)); err == nil {
				t.Fatal("unknown authority shape accepted")
			}
		})
	}
}

func TestEffectPermitMigrationRollsBackEarlierBatchOnUnknownRow(t *testing.T) {
	database, err := sql.Open("sqlite", t.TempDir()+"/fixture.db")
	testutil.FailErr(t, "open fixture", err)
	defer database.Close()
	_, err = database.Exec(`CREATE TABLE checkpoints(id TEXT PRIMARY KEY,kind TEXT,payload_json TEXT)`)
	testutil.FailErr(t, "create fixture", err)
	for i := 0; i < 33; i++ {
		id := string(rune('A' + i))
		payload := retainedOnceFixture
		if i == 32 {
			payload = strings.Replace(payload, `"socket_permit"`, `"future_permit"`, 1)
		}
		_, err = database.Exec(`INSERT INTO checkpoints VALUES(?, 'tool_approval', ?)`, id, payload)
		testutil.FailErr(t, "seed fixture", err)
	}
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin migration", err)
	if err := migrateEffectPermitIdentity(t.Context(), tx); err == nil {
		t.Fatal("unknown later batch accepted")
	}
	testutil.FailErr(t, "rollback refused migration", tx.Rollback())
	var raw string
	testutil.FailErr(t, "read retained first batch", database.QueryRow(`SELECT payload_json FROM checkpoints WHERE id='A'`).Scan(&raw))
	if raw != retainedOnceFixture {
		t.Fatal("refused migration changed earlier checkpoint")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tx, err = database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin cancellation fixture", err)
	if err := migrateEffectPermitIdentity(ctx, tx); err == nil {
		t.Fatal("canceled migration accepted")
	}
	testutil.FailErr(t, "rollback canceled migration", tx.Rollback())
}

func TestEffectPermitMigrationRecognizesActualDirectIPLeaseShape(t *testing.T) {
	const lease = `{"ActionDigest":"semantic-digest","RequestDigest":"requested","ConfinementDigest":"confined","ChatConfinementDigest":"","DeclaredDestinations":["fixture.invalid"],"CommandSummary":"fixture"}`
	input := strings.Replace(retainedOnceFixture, `"kind":"socket_permit"`, `"kind":"direct_ip_permit"`, 1)
	input = strings.Replace(input, `"sockets":[{"approved_path":"/tmp/fixture.sock","resolved_path":"/tmp/fixture.sock"}]`, `"direct_ip_lease":`+lease, 1)
	if _, err := structuredEffectPermitPayload([]byte(input)); err != nil {
		t.Fatalf("actual retained lease rejected: %v", err)
	}
	for name, bad := range map[string]string{
		"future field":     strings.Replace(input, `"RequestDigest":"requested"`, `"RequestDigest":"requested","FutureGrant":true`, 1),
		"wrong field type": strings.Replace(input, `"RequestDigest":"requested"`, `"RequestDigest":42`, 1),
		"incomplete lease": strings.Replace(input, `"RequestDigest":"requested"`, `"RequestDigest":""`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := structuredEffectPermitPayload([]byte(bad)); err == nil {
				t.Fatal("unknown retained direct-IP lease accepted")
			}
		})
	}
}
