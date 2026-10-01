package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReportAvailabilityRequiresEnabledDeliveredTerminalRun(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, delivered := range []bool{false, true} {
			for _, state := range []struct {
				status   api.WorkflowRunStatus
				terminal bool
			}{
				{api.WorkflowRunStatusRunning, false},
				{api.WorkflowRunStatusPaused, false},
				{api.WorkflowRunStatusPausedOnChild, false},
				{api.WorkflowRunStatusComplete, true},
				{api.WorkflowRunStatusFailed, true},
				{api.WorkflowRunStatusCanceled, true},
				{api.WorkflowRunStatusInterrupted, true},
			} {
				manifest := workflowdef.Manifest{Controls: workflowdef.ManifestControls{Report: &workflowdef.ReportControls{Enabled: enabled}}}
				vars := SetGateSatisfied(nil, "topology_report_delivered", delivered)
				got := reportAvailable(&api.WorkflowRun{Status: state.status}, manifest, vars)
				want := enabled && delivered && state.terminal
				if got != want {
					t.Fatalf("enabled=%v delivered=%v status=%s: got %v want %v", enabled, delivered, state.status, got, want)
				}
			}
		}
		// A run failed on a report the host did not accept still offers it.
		notAccepted := &api.WorkflowRun{Status: api.WorkflowRunStatusFailed, Failure: &api.WorkflowFailure{Code: ReportNotAcceptedFailureCode}}
		otherFailure := &api.WorkflowRun{Status: api.WorkflowRunStatusFailed, Failure: &api.WorkflowFailure{Code: "TOPOLOGY_EXECUTION_FAILED"}}
		manifest := workflowdef.Manifest{Controls: workflowdef.ManifestControls{Report: &workflowdef.ReportControls{Enabled: enabled}}}
		if got := reportAvailable(notAccepted, manifest, nil); got != enabled {
			t.Fatalf("enabled=%v not accepted: got %v", enabled, got)
		}
		if reportAvailable(otherFailure, manifest, nil) {
			t.Fatalf("enabled=%v: another failure offered a report", enabled)
		}
	}
}

func TestReportRuntimeAndUIFollowEffectiveManifest(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest := topologyReportTestManifest()
	manifest.Controls.Report = &workflowdef.ReportControls{Enabled: true}
	for i := range manifest.PhaseDefs {
		manifest.PhaseDefs[i].ActivityLabel = manifest.PhaseDefs[i].ID
	}
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start report workflow", err)
	snap := mgr.workflowRuntimeSnapshot(t.Context(), run, manifest, nil)
	if !snap.ReportDocumentEnabled {
		t.Fatal("enabled report phase missing document contract")
	}
	ui, err := mgr.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "read running UI", err)
	if ui.ReportAvailable {
		t.Fatal("running report must not be downloadable")
	}
	testutil.FailErr(t, "deliver report", mgr.MaybeDeliverTopologyReport(t.Context(), run.SessionID, seedTopologyCompletion(t, mgr, run, true, nil)))
	run, err = mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "load completed report run", err)
	ui, err = mgr.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "read completed UI", err)
	available, err := mgr.ReportAvailable(t.Context(), run.ID)
	testutil.FailErr(t, "authorize report", err)
	if !available || !ui.ReportAvailable {
		t.Fatal("delivered report must be available to UI and endpoint")
	}
	if mgr.workflowRuntimeSnapshot(t.Context(), run, manifest, nil).ReportDocumentEnabled {
		t.Fatal("terminal phase must not request another document")
	}
	manifest.Controls.Report.Enabled = false
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	ui, err = mgr.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "read report-disabled UI", err)
	available, err = mgr.ReportAvailable(t.Context(), run.ID)
	testutil.FailErr(t, "authorize disabled report", err)
	if available || ui.ReportAvailable {
		t.Fatal("effective manifest disables document access")
	}
}
