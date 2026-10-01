//go:build integration

package delegation_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowGatePendingSSE(t *testing.T) {
	projectDir := t.TempDir()
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	ch, unsubscribe, err := hub.Subscribe(context.Background(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	depStore := delegation.NewMemoryStore()
	if _, err := depStore.Create(context.Background(), api.Delegation{
		ID:        "dep-sse",
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir,
		Phase: api.DelegationPhaseWorker,
	}, "sess-sse", []api.Leg{{ID: "leg-1"}}); err != nil {
		testutil.FailErr(t, "create delegation", err)
	}

	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	jobID := uuid.NewString()
	workerStore := worker.NewSQLStore(sqlDB)
	testutil.FailErr(t, "InsertTask", workerStore.InsertTask(t.Context(), api.WorkerTask{
		ID: jobID, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir,
		DelegationID: "dep-sse", AgentType: "implementer", ExecutionTarget: api.ExecutionTargetLocal,
		Prompt: "fixture", Brief: "fixture", Status: api.WorkerStatusComplete,
	}))
	bound, err := workerStore.SetWorkerWorkspace(t.Context(), jobID, filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches", jobID), testbaseline.Durable(t, sqlDB, jobID, t.TempDir()))
	testutil.FailErr(t, "SetWorkerWorkspace", err)
	if !bound {
		t.Fatal("expected workspace root CAS to win on a fresh job")
	}
	testutil.FailErr(t, "SetMergeStatus", workerStore.SetMergeStatus(t.Context(), jobID, api.WorkerMergeStatusPending))
	token, claimed, err := workerStore.BeginMergeApply(t.Context(), jobID)
	testutil.FailErr(t, "BeginMergeApply", err)
	if !claimed {
		t.Fatal("expected merge apply claim on a pending overlay")
	}
	scanID := uuid.NewString()
	contract := scancatalog.ScannerEntry{
		ID: "sast-one", Engine: "test", Driver: "test", ScopeKind: string(scancatalog.ScopeSourceDriver),
		Categories: []string{string(api.ScanCategorySAST)},
	}.Contract()
	manifest, executionFingerprint, manifestErr := scancatalog.ExecutionManifest(contract)
	testutil.FailErr(t, "ExecutionManifest", manifestErr)
	testutil.FailErr(t, "CommitPromotion", workerStore.CommitPromotion(t.Context(), jobID, token, worker.PromotionCommit{Plan: obligation.Plan{
		ID: uuid.NewString(), ScanID: scanID, AssessmentID: uuid.NewString(), WorkerJobID: jobID, CanonicalPath: projectDir,
		DelegationID: "dep-sse", ScannerID: "sast-one", ChangedPaths: []string{"main.go"},
		ExecutionManifest: manifest, ExecutionFingerprint: executionFingerprint, FingerprintScheme: api.ScanFingerprintScheme,
		PathScoped: true, Required: true, CreatedAt: time.Now().UTC(),
	}}))

	scanStore := scan.NewSQLStore(sqlDB)
	testutil.FailErr(t, "PublishPending", scantest.Coordinator(t, scanStore, staticHead{sha: "sha-sse"}).PublishPending(t.Context(), scanID))
	checker := &scan.SecurityCloseoutChecker{
		Store:    scanStore,
		Evidence: inspector.NewJSONLStore(inspector.DefaultEvidenceDir),
	}
	grounding := delegation.NewGroundingCoordinator(depStore, nil, nil, delegation.DefaultGroundingConfig(), nil, nil)
	grounding.Events = pub
	grounding.InspectorCloseout = &delegation.InspectorCloseoutGate{
		Store:    depStore,
		Security: checker,
	}

	err = grounding.CheckDelegationCloseout(context.Background(), "dep-sse")
	if !errors.Is(err, scan.ErrGatePending) {
		t.Fatalf("err = %v", err)
	}

	select {
	case envelope := <-ch:
		if envelope.Topic != api.EventTopicWorkflow {
			t.Fatalf("topic = %q", envelope.Topic)
		}
		var ev api.WorkflowEvent
		if err := json.Unmarshal(envelope.Data, &ev); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		if ev.Event != "gate_pending" {
			t.Fatalf("event = %q", ev.Event)
		}
		if ev.BranchInstruction == "" {
			t.Fatal("expected branch_instruction")
		}
	default:
		t.Fatal("expected workflow gate_pending SSE")
	}
}

type staticHead struct{ sha string }

func (s staticHead) HeadSHA(context.Context, string) (string, error) { return s.sha, nil }
