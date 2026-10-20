package migrations_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	_ "modernc.org/sqlite"
)

func TestEffectPermitMigrationRetainsActualValidatedApprovalPlan(t *testing.T) {
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "fixture"}}, Scope: hitl.ActionScope{SessionID: "fixture-session", ProjectID: "fixture-project", ProjectDir: "/fixture"}}
	_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
	primary, cited, reasons := hitl.PresentDecision(decision)
	option := hitl.CurrentActionOption()
	option.Authority = []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthoritySocketPermit, SessionID: "fixture-session", ToolCallID: "actual-model-call", ActionDigest: hitl.GrantKey(action), Sockets: []hitl.ApprovalSocketTarget{{ApprovedPath: "/fixture/service.sock", ResolvedPath: "/fixture/service.sock"}}}}
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectSocketSet, Title: "Fixture socket", Targets: []hitl.ApprovalTarget{{Kind: "socket", Label: "/fixture/service.sock"}}}, hitl.ApprovalPresentation{Action: "Connect to fixture", Impact: "Use one local fixture service", Gate: primary, Cited: cited}, reasons, []hitl.ApprovalOption{option}, hitl.FaceContext{})
	testutil.FailErr(t, "prepare actual owner plan", err)
	testutil.FailErr(t, "validate original owner plan", plan.Validate())
	original, err := json.Marshal(plan)
	testutil.FailErr(t, "encode original owner plan", err)
	payload, err := json.Marshal(map[string]any{"approval_plan": plan})
	testutil.FailErr(t, "encode checkpoint", err)
	database, err := sql.Open("sqlite", t.TempDir()+"/fixture.db")
	testutil.FailErr(t, "open isolated checkpoint", err)
	defer database.Close()
	_, err = database.Exec(`CREATE TABLE checkpoints(id TEXT PRIMARY KEY,kind TEXT,payload_json TEXT)`)
	testutil.FailErr(t, "create checkpoint table", err)
	_, err = database.Exec(`INSERT INTO checkpoints VALUES('fixture','tool_approval',?)`, string(payload))
	testutil.FailErr(t, "persist actual owner plan", err)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin format conversion", err)
	step := migrations.EffectPermitIdentity(migrations.Baseline{Revision: 5})
	testutil.FailErr(t, "apply actual format migration", step.Apply(t.Context(), tx))
	testutil.FailErr(t, "commit fixture conversion", tx.Commit())
	var raw string
	testutil.FailErr(t, "read migrated checkpoint", database.QueryRow(`SELECT payload_json FROM checkpoints WHERE id='fixture'`).Scan(&raw))
	var restored struct {
		Plan hitl.ApprovalPlan `json:"approval_plan"`
	}
	testutil.FailErr(t, "read canonical owner shape", json.Unmarshal([]byte(raw), &restored))
	testutil.FailErr(t, "validate retained owner identity", restored.Plan.Validate())
	encoded, err := json.Marshal(restored.Plan)
	testutil.FailErr(t, "encode canonical restored plan", err)
	if string(original) != string(encoded) || restored.Plan.Options[0].Authority[0].PermitIdentity().ExecutionID != "" {
		t.Fatal("migration changed actual canonical plan or minted native authority")
	}
}
