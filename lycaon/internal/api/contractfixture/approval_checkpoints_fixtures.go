package contractfixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func AssertCheckpointReadSurfaces(t *testing.T, server http.Handler, sessionID, checkpointID string) {
	t.Helper()
	for _, suffix := range []string{"/checkpoints?include_children=true", "/bootstrap"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+sessionID+suffix, nil)
		api.WithTestAuth(req)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("read %s: %d %s", suffix, response.Code, response.Body.String())
		}
		var checkpoints []wire.CheckpointEvent
		if suffix == "/bootstrap" {
			var bootstrap wire.SessionBootstrap
			testutil.FailErr(t, "decode bootstrap", json.Unmarshal(response.Body.Bytes(), &bootstrap))
			checkpoints = bootstrap.Checkpoints
		} else {
			var list wire.CheckpointListResponse
			testutil.FailErr(t, "decode checkpoints", json.Unmarshal(response.Body.Bytes(), &list))
			checkpoints = list.Checkpoints
		}
		if len(checkpoints) != 1 || checkpoints[0].ID != checkpointID {
			t.Fatalf("%s checkpoints = %+v", suffix, checkpoints)
		}
	}
}

func NewCheckpointHandlerFixture(t *testing.T) (*api.Server, *hitl.Checkpoints, *wire.Session) {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB := testdbfixture.Open(t, "chk-api.db")

	reg := project.NewSQLRegistry(sqlDB)
	dir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), reg, dir)
	testutil.FailErr(t, "reg.Create failed", err)

	store := store.NewSQL(sqlDB)
	sess, err := store.Create(t.Context(), wire.CreateSessionRequest{
		Posture:   wire.SessionPostureBuild,
		ProjectID: p.ID,
	}, p.ID)
	testutil.FailErr(t, "create session in store", err)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))

	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core: api.CoreDependencies{
		Store: store, Projects: reg, Sessions: session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)}, Approvals: api.ApprovalsDependencies{Checkpoints: mgr}}), nil, api.TestAPIToken)
	return srv, mgr, sess
}

func RequestExplicitAPIApprovalCheckpoint(t *testing.T, mgr hitl.CheckpointManager, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	t.Helper()
	if req.Kind != wire.CheckpointKindToolApproval || req.Decision != nil || req.ApprovalPlan != nil {
		t.Fatal("explicit approval fixture requires an uncompiled tool-approval request")
	}
	verdict, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	if verdict != gate.Ask || decision == nil || decision.Primary != wire.GateExplicitApprovalRequest {
		t.Fatalf("explicit approval fixture decision = %s/%+v", verdict, decision)
	}
	req.Decision = decision
	return mgr.RequestCheckpoint(t.Context(), req)
}
