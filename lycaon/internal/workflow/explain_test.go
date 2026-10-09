package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// progressObligation is a scan-like kind whose pending work has a pass to watch.
type progressObligation struct {
	stubWorkflowObligation
	assessmentID string
}

func (p *progressObligation) ExplainFullPass(_ context.Context, run *api.WorkflowRun, _ map[string]any) (*api.WorkflowExplainFullPass, error) {
	return &api.WorkflowExplainFullPass{AssessmentID: p.assessmentID, ProjectID: run.ProjectID}, nil
}

func explainTestManifest(explain *workflowdef.PhaseExplain) workflowdef.Manifest {
	m := obligationTestManifest()
	for i := range m.PhaseDefs {
		if m.PhaseDefs[i].ID == "ingest" {
			m.PhaseDefs[i].Explain = explain
		}
	}
	return m
}

// Entering a held phase writes its note after the obligation started, so the
// note names the pass that obligation requested.
func TestPhaseEntryWritesExplainNoteWithProgress(t *testing.T) {
	mgr, sessStore, _, _ := testManager(t)
	obligation := &progressObligation{
		stubWorkflowObligation: stubWorkflowObligation{status: api.WorkflowRunObligation{Kind: "scan", Status: api.ObligationStatusPending}},
		assessmentID:           "6f2c0c56-1f43-4a4e-9f55-1d1b2c3d4e5f",
	}
	wireTestObligation(t, mgr, &obligation.stubWorkflowObligation)
	mgr.Obligations.Register(obligation)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"obligationtest@1.0.0": explainTestManifest(&workflowdef.PhaseExplain{Summary: "A full scan runs first", Body: "Why it runs."}),
	})

	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "obligationtest", "1.0.0")
	testutil.FailErr(t, "start run", err)

	note := onlyExplainNote(t, sessStore)
	meta := note.WorkflowExplain
	if meta.PhaseID != "ingest" || meta.Summary != "A full scan runs first" || meta.Body != "Why it runs." {
		t.Fatalf("explain meta = %+v", meta)
	}
	if note.WorkflowRunID != run.ID || note.Role != api.MessageRoleSystem || note.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("explain row = run %q role %q visibility %q", note.WorkflowRunID, note.Role, note.Visibility)
	}
	if meta.Progress == nil || meta.Progress.FullPass == nil || meta.Progress.FullPass.AssessmentID != obligation.assessmentID {
		t.Fatalf("explain progress = %+v", meta.Progress)
	}
	if !api.IsTranscriptChromeMessage(note) {
		t.Fatal("an explain note must stay out of model history")
	}
}

// A kind with nothing to watch still gets its note; the note only explains.
func TestPhaseEntryWritesExplainNoteWithoutProgress(t *testing.T) {
	mgr, sessStore, _, _ := testManager(t)
	wireTestObligation(t, mgr, &stubWorkflowObligation{status: api.WorkflowRunObligation{Kind: "scan", Status: api.ObligationStatusPending}})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"obligationtest@1.0.0": explainTestManifest(&workflowdef.PhaseExplain{Summary: "A scan runs first", Body: "Why."}),
	})
	_, err := startRun(context.Background(), mgr, "sess-1", "obligationtest", "1.0.0")
	testutil.FailErr(t, "start run", err)
	if note := onlyExplainNote(t, sessStore); note.WorkflowExplain.Progress != nil {
		t.Fatalf("progress = %+v, want none", note.WorkflowExplain.Progress)
	}
}

func TestPhaseEntryWithoutExplainWritesNoNote(t *testing.T) {
	mgr, sessStore, _, _ := testManager(t)
	wireTestObligation(t, mgr, &stubWorkflowObligation{status: api.WorkflowRunObligation{Kind: "scan", Status: api.ObligationStatusPending}})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": obligationTestManifest()})
	_, err := startRun(context.Background(), mgr, "sess-1", "obligationtest", "1.0.0")
	testutil.FailErr(t, "start run", err)
	msgs, err := sessStore.GetMessages(context.Background(), "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	for _, msg := range msgs {
		if api.IsWorkflowExplainMessage(msg) {
			t.Fatalf("unexpected explain note %+v", msg)
		}
	}
}

func onlyExplainNote(t *testing.T, sessStore session.Store) api.Message {
	t.Helper()
	msgs, err := sessStore.GetMessages(context.Background(), "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	var notes []api.Message
	for _, msg := range msgs {
		if api.IsWorkflowExplainMessage(msg) {
			notes = append(notes, msg)
		}
	}
	if len(notes) != 1 || notes[0].WorkflowExplain == nil {
		t.Fatalf("explain notes = %+v, want one", notes)
	}
	return notes[0]
}

type stubTopologyLegs struct {
	topologyID  string
	stagePhases map[string]string
}

func (s *stubTopologyLegs) RunTopologyLegs(_ context.Context, _ *api.WorkflowRun, topologyID string, stagePhases map[string]string) ([]api.WorkflowTopologyLeg, error) {
	s.topologyID, s.stagePhases = topologyID, stagePhases
	return []api.WorkflowTopologyLeg{{ID: "hunt_edges", Stage: "hunt_edges", PhaseID: "hunt", Label: "Edge case hunt", Status: api.LegStatusRunning}}, nil
}

// A topology phase's note names the stages it binds; their legs arrive with
// the run's projection, which reads the manifest's stage bindings.
func TestTopologyPhaseExplainNamesItsStagesAndRunProjectsLegs(t *testing.T) {
	mgr, sessStore, _, _ := testManagerWithRegistry(t)
	legs := &stubTopologyLegs{}
	mgr.Presentation.TopologyLegs = legs
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "bugbash", "1.0.0")
	testutil.FailErr(t, "start bugbash run", err)

	note := onlyExplainNote(t, sessStore)
	topology := note.WorkflowExplain.Progress
	if topology == nil || topology.Topology == nil {
		t.Fatalf("explain progress = %+v", topology)
	}
	if got := topology.Topology.Stages; len(got) != 3 || got[0] != "hunt_correctness" {
		t.Fatalf("stages = %v", got)
	}

	ui, err := mgr.Presentation.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if len(ui.TopologyLegs) != 1 || ui.TopologyLegs[0].ID != "hunt_edges" {
		t.Fatalf("topology legs = %+v", ui.TopologyLegs)
	}
	if legs.topologyID != "bugbash" || legs.stagePhases["triage"] != "triage" || legs.stagePhases["hunt_races"] != "hunt" {
		t.Fatalf("leg source read topology %q stages %v", legs.topologyID, legs.stagePhases)
	}
}
