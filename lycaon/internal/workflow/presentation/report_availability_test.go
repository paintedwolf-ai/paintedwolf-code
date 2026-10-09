package presentation_test

import (
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"testing"
)

func TestReportRuntimeAndUIFollowEffectiveManifest(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest := topologyReportTestManifest()
	manifest.Controls.Report = &workflowdef.ReportControls{Enabled: true}
	for i := range manifest.PhaseDefs {
		manifest.PhaseDefs[i].ActivityLabel = manifest.PhaseDefs[i].ID
	}
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start report workflow", err)
	snap := mgr.Snapshots.Project(t.Context(), run, manifest, nil)
	if !snap.ReportDocumentEnabled {
		t.Fatal("enabled report phase missing document contract")
	}
	ui, err := mgr.Presentation.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "read running UI", err)
	if ui.ReportAvailable {
		t.Fatal("running report must not be downloadable")
	}
	testutil.FailErr(t, "deliver report", mgr.Reports.MaybeDeliverTopologyReport(t.Context(), run.SessionID, seedTopologyCompletion(t, mgr, run, true, nil)))
	run, err = mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "load completed report run", err)
	ui, err = mgr.Presentation.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "read completed UI", err)
	available, err := mgr.Presentation.ReportAvailable(t.Context(), run.ID)
	testutil.FailErr(t, "authorize report", err)
	if !available || !ui.ReportAvailable {
		t.Fatal("delivered report must be available to UI and endpoint")
	}
	if mgr.Snapshots.Project(t.Context(), run, manifest, nil).ReportDocumentEnabled {
		t.Fatal("terminal phase must not request another document")
	}
	manifest.Controls.Report.Enabled = false
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"reporttest@1.0.0": manifest})
	ui, err = mgr.Presentation.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "read report-disabled UI", err)
	available, err = mgr.Presentation.ReportAvailable(t.Context(), run.ID)
	testutil.FailErr(t, "authorize disabled report", err)
	if available || ui.ReportAvailable {
		t.Fatal("effective manifest disables document access")
	}
}
