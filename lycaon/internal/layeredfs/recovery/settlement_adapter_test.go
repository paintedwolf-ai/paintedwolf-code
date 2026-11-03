package recovery_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

type mockProcessMountRig struct {
	failProcessSettle, failMountSettle bool
	settledWorkers, settledMountViews  []string
	settledPIDs                        []int
}

func (m *mockProcessMountRig) SettleProcesses(workerID, viewID string, pids []int) (recovery.ProcessSettlementReceipt, error) {
	m.settledWorkers = append(m.settledWorkers, workerID)
	m.settledPIDs = append(m.settledPIDs, pids...)
	if m.failProcessSettle {
		return recovery.ProcessSettlementReceipt{WorkerID: workerID, ViewID: viewID, SurvivingPIDs: pids, Settled: false}, errors.New("writer process still active")
	}
	return recovery.ProcessSettlementReceipt{WorkerID: workerID, ViewID: viewID, StoppedPIDs: pids, Settled: true}, nil
}

func (m *mockProcessMountRig) ReconcileWorkerMount(rec recovery.MountIdentityRecord) (recovery.MountSettlementReceipt, error) {
	m.settledMountViews = append(m.settledMountViews, rec.ViewID)
	if m.failMountSettle {
		return recovery.MountSettlementReceipt{ViewID: rec.ViewID, MountID: rec.MountID, BusyPaths: rec.MountPaths, Settled: false}, errors.New("mount busy")
	}
	return recovery.MountSettlementReceipt{ViewID: rec.ViewID, MountID: rec.MountID, UnmountedPaths: rec.MountPaths, ConnectionAborted: true, Settled: true}, nil
}

func TestStorageLossOrderingENOSPCAndCorruptMetadata(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := t.Context()

	var simulateENOSPC bool
	coord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir:         filepath.Join(fx.baseDir, "recovery-enospc"),
		CheckpointStore: fx.cpStore,
		WriteHook: func(_, _ string, _ []byte) error {
			if simulateENOSPC {
				return syscall.ENOSPC
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := fx.mountedAdp.Prepare(ctx, fx.prepareRequest("view-enospc"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, view.ViewID) }()

	qpApp := layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}
	_, _ = view.Core.WriteFile(qpApp, []byte("package main // durable-cp1\n"), 0o644)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             view.Core,
		Store:            fx.cpStore,
		WorkerID:         "worker-enospc",
		TaskID:           "t-1",
		ProjectID:        "p-1",
		SourceGeneration: "g-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-durable-1"}); err != nil {
		t.Fatal(err)
	}

	if err := coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion:             recovery.FormatVersion,
		WorkerID:                  "worker-enospc",
		ViewID:                    view.ViewID,
		TaskID:                    "t-1",
		ProjectID:                 "p-1",
		AdapterID:                 fx.mountedAdp.ID(),
		LastPublishedCheckpointID: "cp-durable-1",
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate disk-full (ENOSPC) on subsequent transition update: prior valid journal must remain intact.
	simulateENOSPC = true
	if err := coord.RecordTransition("worker-enospc", view.ViewID, recovery.PhasePublication, recovery.StateCommitted, "cp-unwritten", "publish", nil, false); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected ENOSPC, got %v", err)
	}
	simulateENOSPC = false

	loaded, err := coord.LoadWorkerJournal("worker-enospc")
	if err != nil || loaded.LastPublishedCheckpointID != "cp-durable-1" {
		t.Fatalf("expected prior valid journal preserved after ENOSPC, got %+v err=%v", loaded, err)
	}

	// Unknown format_version (99) must be retained on disk and refused with ErrUnsupportedFormatVersion.
	unknownPath := filepath.Join(fx.baseDir, "recovery-enospc", "journals", "worker-future.json")
	unknownRaw := []byte(`{"format_version":99,"worker_id":"worker-future","project_id":"p-1"}`)
	if err := os.WriteFile(unknownPath, unknownRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := coord.LoadWorkerJournal("worker-future"); !errors.Is(err, recovery.ErrUnsupportedFormatVersion) {
		t.Fatalf("expected ErrUnsupportedFormatVersion, got %v", err)
	}
	if afterRaw, rErr := os.ReadFile(unknownPath); rErr != nil || !bytes.Equal(afterRaw, unknownRaw) {
		t.Fatalf("expected unknown metadata retained untouched on disk")
	}

	// Corrupt/unknown-field JSON must be retained on disk and refused with ErrCorruptRecoveryMetadata.
	corruptPath := filepath.Join(fx.baseDir, "recovery-enospc", "journals", "worker-corrupt.json")
	corruptRaw := []byte(`{"format_version":1,"worker_id":"worker-corrupt","project_id":"p-1","unexpected_field":true}`)
	if err := os.WriteFile(corruptPath, corruptRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := coord.LoadWorkerJournal("worker-corrupt"); !errors.Is(err, recovery.ErrCorruptRecoveryMetadata) {
		t.Fatalf("expected ErrCorruptRecoveryMetadata, got %v", err)
	}
	if afterCorrupt, rErr := os.ReadFile(corruptPath); rErr != nil || !bytes.Equal(afterCorrupt, corruptRaw) {
		t.Fatalf("expected corrupt metadata retained untouched on disk")
	}
}

func TestSameBootSurvivingWriterSettlementAndScopedReconciliation(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := t.Context()
	rig := &mockProcessMountRig{failProcessSettle: true}

	coord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir:         filepath.Join(fx.baseDir, "recovery-settle"),
		CheckpointStore: fx.cpStore,
		CurrentBootID:   "boot-same",
		ProcessCtrl:     rig,
		MountReconciler: rig,
	})
	if err != nil {
		t.Fatal(err)
	}

	view1, err := fx.mountedAdp.Prepare(ctx, fx.prepareRequest("view-w1"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, view1.ViewID) }()

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             view1.Core,
		Store:            fx.cpStore,
		WorkerID:         "worker-1",
		TaskID:           "t-1",
		ProjectID:        "p-1",
		SourceGeneration: "g-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = view1.Core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}, []byte("package main // w1\n"), 0o644)
	if _, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-w1"}); err != nil {
		t.Fatal(err)
	}

	// Register both worker-1 and worker-2 under the same runtime prefix.
	for _, w := range []struct {
		workerID string
		viewID   string
		pids     []int
	}{
		{"worker-1", "view-w1", []int{4101, 4102}},
		{"worker-2", "view-w2", []int{5201}},
	} {
		_ = coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
			FormatVersion:             recovery.FormatVersion,
			WorkerID:                  w.workerID,
			ViewID:                    w.viewID,
			TaskID:                    "t-1",
			ProjectID:                 "p-1",
			AdapterID:                 fx.mountedAdp.ID(),
			BootID:                    "boot-same",
			LastPublishedCheckpointID: "cp-w1",
		})
		_ = coord.RecordMountIdentity(recovery.MountIdentityRecord{
			ViewID:         w.viewID,
			WorkerID:       w.workerID,
			AdapterID:      fx.mountedAdp.ID(),
			MountID:        "mnt-" + w.viewID,
			MountPaths:     []string{"/tmp/shared-prefix/" + w.viewID},
			RuntimeViewDir: view1.RuntimeViewDir,
			BackingDir:     view1.BackingDir,
			BootID:         "boot-same",
			RecordedPIDs:   w.pids,
		})
	}

	// 1. Failing process settlement blocks recovery and retains custody.
	_, err = coord.RecoverWorkerView(ctx, recovery.RecoverRequest{
		WorkerID:   "worker-1",
		NewViewID:  "view-w1-rec",
		Adapter:    fx.mountedAdp,
		PrepareReq: fx.prepareRequest("view-w1-rec"),
	})
	var unsettled *recovery.UnresolvedSettlementError
	if !errors.As(err, &unsettled) || !unsettled.RetainedCustody || !slices.Equal(unsettled.SurvivingPIDs, []int{4101, 4102}) {
		t.Fatalf("expected UnresolvedSettlementError for surviving writers, got %v", err)
	}

	// 2. Failing mount settlement on CancelAndRelease retains custody and blocks premature deletion.
	rig.failProcessSettle = false
	rig.failMountSettle = true
	_, err = coord.CancelAndRelease(ctx, recovery.CancelReleaseRequest{
		WorkerID: "worker-1",
		ViewID:   "view-w1",
		View:     view1,
		Adapter:  fx.mountedAdp,
	})
	if !errors.As(err, &unsettled) || !unsettled.MountBusy || !unsettled.RetainedCustody {
		t.Fatalf("expected UnresolvedSettlementError for busy mount, got %v", err)
	}

	// 3. Successful settlement touches ONLY worker-1's PIDs and mount, never worker-2 (5201).
	rig.failMountSettle = false
	rig.settledPIDs = nil
	rig.settledMountViews = nil
	recRes, err := coord.RecoverWorkerView(ctx, recovery.RecoverRequest{
		WorkerID:   "worker-1",
		NewViewID:  "view-w1-rec2",
		Adapter:    fx.mountedAdp,
		PrepareReq: fx.prepareRequest("view-w1-rec2"),
	})
	if err != nil {
		t.Fatalf("RecoverWorkerView after settlement: %v", err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, recRes.ViewID) }()

	if slices.Contains(rig.settledPIDs, 5201) || slices.Contains(rig.settledMountViews, "view-w2") {
		t.Fatalf("reconciliation touched unrelated worker-2: pids=%v views=%v", rig.settledPIDs, rig.settledMountViews)
	}
}

func TestRebuildWithAdapterAndReleaseSeparation(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := t.Context()
	eagerAdp := adapter.NewEagerCopyAdapter(adapter.ReasonNone)

	viewEager, err := eagerAdp.Prepare(ctx, fx.prepareRequest("view-eager-1"))
	if err != nil {
		t.Fatal(err)
	}
	qpApp := layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}
	_, _ = viewEager.Core.WriteFile(qpApp, []byte("package main // adapter-switch-v1\n"), 0o644)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             viewEager.Core,
		Store:            fx.cpStore,
		WorkerID:         "worker-switch",
		TaskID:           "t-sw",
		ProjectID:        "p-sw",
		SourceGeneration: "g-sw",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-sw-1"}); err != nil {
		t.Fatal(err)
	}
	_ = fx.coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion:             recovery.FormatVersion,
		WorkerID:                  "worker-switch",
		ViewID:                    viewEager.ViewID,
		TaskID:                    "t-sw",
		ProjectID:                 "p-sw",
		AdapterID:                 eagerAdp.ID(),
		LastPublishedCheckpointID: "cp-sw-1",
	})

	// Unsealed mutation in viewEager must refuse RebuildWithAdapter when CaptureBeforeSwitch is false.
	_, _ = viewEager.Core.WriteFile(qpApp, []byte("package main // adapter-switch-v2-unsealed\n"), 0o644)
	if _, err := fx.coord.RebuildWithAdapter(ctx, recovery.RebuildAdapterRequest{
		WorkerID:            "worker-switch",
		CurrentView:         viewEager,
		CurrentAdapter:      eagerAdp,
		TargetAdapter:       fx.mountedAdp,
		NewViewID:           "view-mounted-sw",
		PrepareReq:          fx.prepareRequest("view-mounted-sw"),
		CaptureBeforeSwitch: false,
	}); !errors.Is(err, recovery.ErrActiveUnsealedWork) {
		t.Fatalf("expected ErrActiveUnsealedWork when unsealed work exists, got %v", err)
	}

	// CaptureBeforeSwitch=true captures v2 and reconstructs into MountedAdapter cleanly.
	viewMounted, err := fx.coord.RebuildWithAdapter(ctx, recovery.RebuildAdapterRequest{
		WorkerID:            "worker-switch",
		CurrentView:         viewEager,
		CurrentAdapter:      eagerAdp,
		TargetAdapter:       fx.mountedAdp,
		NewViewID:           "view-mounted-sw",
		PrepareReq:          fx.prepareRequest("view-mounted-sw"),
		Publisher:           pub,
		CaptureBeforeSwitch: true,
	})
	if err != nil {
		t.Fatalf("RebuildWithAdapter with capture: %v", err)
	}
	got, err := viewMounted.Core.ReadFile(qpApp)
	if err != nil || !bytes.Equal(got, []byte("package main // adapter-switch-v2-unsealed\n")) {
		t.Fatalf("expected v2 reconstructed on new adapter, got %q err=%v", string(got), err)
	}

	// CancelAndRelease releases the mount without deleting the checkpoint.
	if _, err := fx.coord.CancelAndRelease(ctx, recovery.CancelReleaseRequest{
		WorkerID: "worker-switch",
		ViewID:   viewMounted.ViewID,
		View:     viewMounted,
		Adapter:  fx.mountedAdp,
	}); err != nil {
		t.Fatalf("CancelAndRelease: %v", err)
	}
	if _, err := fx.coord.VerifyCheckpointReconstructible(ctx, "cp-sw-1"); err != nil {
		t.Fatalf("expected cp-sw-1 retained after mount release, got %v", err)
	}
}
