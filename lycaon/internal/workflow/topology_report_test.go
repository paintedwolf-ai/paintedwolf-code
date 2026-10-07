package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func topologyReportTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "reporttest",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "report",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{"topology_report_delivered"},
				Next:         "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}

func TestMaybeDeliverTopologyReportCompletesRun(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest := topologyReportTestManifest()
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "reporttest", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	if run.CurrentPhase != "report" {
		t.Fatalf("phase = %q want report", run.CurrentPhase)
	}

	testutil.FailErr(t, "MaybeDeliverTopologyReport", mgr.MaybeDeliverTopologyReport(ctx, "sess-1", seedTopologyCompletion(t, mgr, run, false, nil)))
	run, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("status = %q want complete (phase=%q)", run.Status, run.CurrentPhase)
	}
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", run.CurrentPhase)
	}
	if run.CompletedAt == nil {
		t.Fatal("completed_at must be set")
	}
}

func seedTopologyCompletion(t *testing.T, mgr *RunManager, run *api.WorkflowRun, document bool, edit func(*api.Message)) string {
	t.Helper()
	scope := api.CompletionReportScopePhase
	if document {
		scope = api.CompletionReportScopeRun
	}
	msg := api.Message{
		ID: uuid.NewString(), Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport,
		Content: "Completed assessment.", CreatedAt: time.Now().UTC(), WorkflowRunID: run.ID,
		Visibility: api.MessageVisibilityTranscript, DraftStatus: api.DraftStatusCommitted,
		Grounding:        &api.CitationGrounding{Traced: true},
		CompletionReport: &api.CompletionReportMeta{Scope: scope, Phase: run.CurrentPhase},
	}
	if edit != nil {
		edit(&msg)
	}
	testutil.FailErr(t, "persist phase completion", mgr.Sessions.AppendMessages(t.Context(), run.SessionID, msg))
	return msg.ID
}

func TestReportDeliveryRequiresTheCommittedPhaseCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*api.Message)
	}{
		{"earlier phase", func(m *api.Message) { m.CompletionReport.Phase = "research" }},
		{"ordinary reply", func(m *api.Message) { m.CompletionReport.Scope = api.CompletionReportScopePhase }},
		{"missing binding", func(m *api.Message) { m.CompletionReport = nil }},
		{"missing grounding", func(m *api.Message) { m.Grounding = nil }},
		{"hidden draft", func(m *api.Message) { m.Visibility = api.MessageVisibilityInternal }},
		{"live draft", func(m *api.Message) { m.DraftStatus = api.DraftStatusLive }},
		{"withdrawn draft", func(m *api.Message) { m.DraftStatus = api.DraftStatusWithdrawn }},
		{"superseded reply", func(m *api.Message) { m.Kind = api.MessageKindSuperseded }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _, _, _ := testManagerWithRegistry(t)
			manifest := topologyReportTestManifest()
			manifest.Controls.Report = &workflowdef.ReportControls{Enabled: true}
			mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
			run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
			testutil.FailErr(t, "start report run", err)
			id := seedTopologyCompletion(t, mgr, run, true, tc.edit)
			testutil.FailErr(t, "reject unrelated completion", mgr.MaybeDeliverTopologyReport(t.Context(), run.SessionID, id))
			after, err := mgr.Get(t.Context(), run.ID)
			testutil.FailErr(t, "read report run", err)
			vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
			testutil.FailErr(t, "read report gates", err)
			if after.Status != api.WorkflowRunStatusRunning || gateSatisfiedInVars(vars, "topology_report_delivered") {
				t.Fatalf("unrelated completion delivered report: %+v", after)
			}
			valid := seedTopologyCompletion(t, mgr, run, true, nil)
			testutil.FailErr(t, "deliver valid completion", mgr.MaybeDeliverTopologyReport(t.Context(), run.SessionID, valid))
			after, err = mgr.Get(t.Context(), run.ID)
			testutil.FailErr(t, "read delivered run", err)
			if after.Status != api.WorkflowRunStatusComplete {
				t.Fatalf("valid report did not complete run: %+v", after)
			}
		})
	}
}

func withDefects(m *api.Message) {
	m.CompletionReport.Defects = []api.CompletionReportDefect{{
		Code: api.CompletionReportDefectCodeClaimUnreported, Reason: "claims need a finding", Subjects: []string{"c1 (failed)"}, Count: 1,
	}}
}

// A report stored with document defects ends its run as not accepted,
// without satisfying the delivery gate, and stays downloadable.
func TestReportWithDefectsFailsTheRunAsNotAccepted(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest := topologyReportTestManifest()
	manifest.Controls.Report = &workflowdef.ReportControls{Enabled: true}
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start report run", err)
	id := seedTopologyCompletion(t, mgr, run, true, withDefects)
	testutil.FailErr(t, "settle unaccepted report", mgr.MaybeDeliverTopologyReport(t.Context(), run.SessionID, id))
	after, err := mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read settled run", err)
	if after.Status != api.WorkflowRunStatusFailed || after.Failure == nil ||
		after.Failure.Code != ReportNotAcceptedFailureCode || after.Failure.Phase != "report" || after.Failure.Retryable {
		t.Fatalf("run = %+v failure = %+v, want failed as not accepted in report", after, after.Failure)
	}
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read report gates", err)
	if gateSatisfiedInVars(vars, "topology_report_delivered") {
		t.Fatal("an unaccepted report satisfied the delivery gate")
	}
	available, err := mgr.ReportAvailable(t.Context(), run.ID)
	testutil.FailErr(t, "authorize unaccepted report", err)
	if !available {
		t.Fatal("unaccepted report must stay downloadable")
	}
}

func TestReportWithAdvisoryDefectCompletesRun(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest := topologyReportTestManifest()
	manifest.Controls.Report = &workflowdef.ReportControls{Enabled: true}
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start report run", err)
	advisory := func(m *api.Message) {
		m.CompletionReport.Defects = []api.CompletionReportDefect{{
			Code:     api.CompletionReportDefectCodeFenceUnreadable,
			Reason:   "unrecognized non-substantive member",
			Subjects: []string{"headline_note"},
			Count:    1,
		}}
	}
	id := seedTopologyCompletion(t, mgr, run, true, advisory)
	testutil.FailErr(t, "deliver report with advisory defect", mgr.MaybeDeliverTopologyReport(t.Context(), run.SessionID, id))
	after, err := mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read settled run", err)
	if after.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("run status = %q, want complete", after.Status)
	}
	if after.CurrentPhase != "done" {
		t.Fatalf("phase = %q, want done", after.CurrentPhase)
	}
}

func TestLateReportCannotCompleteReplacementRun(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest := topologyReportTestManifest()
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	old, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start original run", err)
	messageID := seedTopologyCompletion(t, mgr, old, false, nil)
	_, err = mgr.Cancel(t.Context(), old.ID, "replaced")
	testutil.FailErr(t, "cancel original run", err)
	replacement, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start replacement run", err)
	testutil.FailErr(t, "handle late completion", mgr.MaybeDeliverTopologyReport(t.Context(), old.SessionID, messageID))
	after, err := mgr.Get(t.Context(), replacement.ID)
	testutil.FailErr(t, "read replacement", err)
	if after.Status != api.WorkflowRunStatusRunning || after.CurrentPhase != "report" {
		t.Fatalf("late completion advanced replacement: %+v", after)
	}
}

func TestSuccessfulTurnWithoutCloseoutDoesNotDeliverReport(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest := topologyReportTestManifest()
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start report run", err)
	testutil.FailErr(t, "finish turn without closeout", mgr.MaybeDeliverTopologyReport(t.Context(), run.SessionID, ""))
	after, err := mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read unfinished run", err)
	if after.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("turn without closeout completed run: %+v", after)
	}
}

func TestOrphanRecoveryPreservesCommittedReportDelivery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		edit      func(*api.Message)
		want      api.WorkflowRunStatus
		available bool
	}{
		{"earlier phase", func(m *api.Message) { m.CompletionReport.Phase = "research" }, api.WorkflowRunStatusInterrupted, false},
		{"committed report", nil, api.WorkflowRunStatusComplete, true},
		{"unaccepted report", withDefects, api.WorkflowRunStatusFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _, _, _ := testManagerWithRegistry(t)
			manifest := topologyReportTestManifest()
			manifest.Controls.Report = &workflowdef.ReportControls{Enabled: true}
			mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
			run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
			testutil.FailErr(t, "start report run", err)
			seedTopologyCompletion(t, mgr, run, true, tc.edit)
			mgr.OrphanReconcileBefore = time.Now().UTC().Add(time.Second)
			testutil.FailErr(t, "recover after committed message", mgr.ReconcileOrphanedRuns(t.Context(), run.SessionID))
			after, err := mgr.Get(t.Context(), run.ID)
			testutil.FailErr(t, "read recovered run", err)
			if after.Status != tc.want {
				t.Fatalf("recovered status = %s, want %s", after.Status, tc.want)
			}
			available, err := mgr.ReportAvailable(t.Context(), run.ID)
			testutil.FailErr(t, "read recovered report availability", err)
			if available != tc.available {
				t.Fatalf("recovered report available = %v, want %v", available, tc.available)
			}
		})
	}
}
