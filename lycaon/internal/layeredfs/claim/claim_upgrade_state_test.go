package claim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestFormatV1UpgradeRetainsMaterializedSemanticsAndUnknownVersionsFailWithoutWrites(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	v1Fixture := LegacyBranchMetadataV1{
		FormatVersion:    FormatVersionV1,
		JobID:            "job-legacy-42",
		ProjectID:        "proj-legacy",
		SourceGeneration: 19,
		BranchRoot:       filepath.Join(tmp, "legacy-branch", "primary"),
		Roots: []RootMappingRecord{
			{
				RootID:        "primary",
				Label:         "primary",
				SourcePath:    filepath.Join(tmp, "trunk"),
				WorkspacePath: filepath.Join(tmp, "legacy-branch", "primary"),
				IsPrimary:     true,
			},
		},
		FocusPathHints: []string{"cmd/app/main.go"},
		CapturedBases: []layeredfs.BaseCapture{
			{
				Path:   layeredfs.QualifiedPath{RootID: "primary", Rel: "cmd/app/main.go"},
				Layer:  layeredfs.LayerTrunk,
				Kind:   layeredfs.EntryRegular,
				Mode:   0o644,
				Size:   128,
				SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		},
		LastCheckpointID: "cp-legacy-only-result",
	}
	v1Path := filepath.Join(tmp, "v1-meta.json")
	rawV1, err := json.MarshalIndent(v1Fixture, "", "  ")
	testutil.FailErr(t, "marshal v1", err)
	testutil.FailErr(t, "write v1", os.WriteFile(v1Path, rawV1, 0o600))

	coord := NewCoordinator(nil)
	cv, upgraded, err := coord.LoadOrUpgradeRetainedView(v1Path)
	testutil.FailErr(t, "LoadOrUpgradeRetainedView v1", err)
	if !upgraded {
		t.Fatal("expected upgraded=true for Format-v1 fixture")
	}
	if cv.Metadata.FormatVersion != FormatVersionV2 ||
		cv.Metadata.Adapter.AdapterID != adapter.AdapterEagerCopy ||
		cv.Metadata.Adapter.PreparationKind != adapter.PreparationMaterialized ||
		cv.Metadata.Adapter.SourceVisibility != adapter.VisibilityFixed ||
		!cv.Metadata.Adapter.IsLegacyMaterialized {
		t.Fatalf("v1 materialized branch must retain materialized fixed visibility semantics, got %+v", cv.Metadata.Adapter)
	}
	if len(cv.Metadata.LegacyCapturedBases) != 1 || cv.Metadata.Checkpoints.LastPublishedCheckpointID != "cp-legacy-only-result" {
		t.Fatalf("upgrade must preserve captured bases and retained checkpoint result, got %+v", cv.Metadata)
	}

	upgradedMeta, err := UpgradeMetadataFile(v1Path)
	testutil.FailErr(t, "UpgradeMetadataFile", err)
	if upgradedMeta.FormatVersion != FormatVersionV2 || !upgradedMeta.Adapter.IsLegacyMaterialized {
		t.Fatalf("expected persisted Format-v2 legacy materialized record, got %+v", upgradedMeta)
	}

	// Attempting to relabel a legacy materialized branch as a live mounted view must be refused.
	relabeled := upgradedMeta
	relabeled.Adapter.AdapterID = adapter.AdapterFUSELinux
	relabeled.Adapter.PreparationKind = adapter.PreparationMounted
	relabeled.Adapter.SourceVisibility = adapter.VisibilityLive
	if err := SaveViewMetadata(filepath.Join(tmp, "relabeled.json"), relabeled); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata when relabeling legacy materialized branch as mounted, got %v", err)
	}

	// Unknown versions and unknown shapes must fail without mutating the file on disk.
	for _, tc := range []struct {
		name    string
		payload string
		wantErr error
	}{
		{"v0", `{"format_version": 0, "job_id": "x"}`, ErrUnsupportedMetadataVersion},
		{"v3", `{"format_version": 3, "identity": {"worker_id": "w", "view_id": "v"}}`, ErrUnsupportedMetadataVersion},
		{"unknown_field", `{"format_version": 2, "unknown_shape": true}`, ErrInvalidMetadata},
	} {
		badPath := filepath.Join(tmp, "bad-"+tc.name+".json")
		testutil.FailErr(t, "write "+tc.name, os.WriteFile(badPath, []byte(tc.payload), 0o600))
		if _, _, lErr := LoadViewMetadata(badPath); !errors.Is(lErr, tc.wantErr) {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.wantErr, lErr)
		}
		if _, uErr := UpgradeMetadataFile(badPath); !errors.Is(uErr, tc.wantErr) {
			t.Fatalf("%s upgrade: expected %v, got %v", tc.name, tc.wantErr, uErr)
		}
		afterBytes, rErr := os.ReadFile(badPath)
		testutil.FailErr(t, "read after "+tc.name, rErr)
		if string(afterBytes) != tc.payload {
			t.Fatalf("%s: refused metadata file was mutated on disk: got %q, want %q", tc.name, string(afterBytes), tc.payload)
		}
	}
}

func TestHostAndDenWorkspaceStateAndReceiptsDistinguishFallbackGitCommandAndCheckpoint(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	gittest.Init(t, trunk)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(trunk, "hello.txt"), []byte("hello\n"), 0o644))
	gittest.CommitAll(t, trunk, "initial commit")

	// Configure selector where primary mounted adapter fails with ReasonFUSEDeviceUnavailable so eagercopy fallback is chosen.
	unsupportedPrimary := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterFUSELinux,
		TargetOS:  "linux",
		MountProbeFn: func(_ context.Context, _, _ string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonFUSEDeviceUnavailable, "/dev/fuse missing", errors.New("no fuse")
		},
	})
	sel := adapter.NewSelector("linux", unsupportedPrimary, unsupportedPrimary, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	sel.BudgetFor = adapter.ZeroBudget
	coord := NewCoordinator(sel)

	cv, snap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity:       ClaimIdentity{WorkerID: "w-fb", ProjectID: "proj-fb", ViewID: "v-fb", SourceGeneration: 3},
		Storage:        ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:         []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
		FocusPathHints: []string{"hello.txt"},
	})
	testutil.FailErr(t, "ClaimWorkerView eagercopy fallback", err)
	defer func() { _ = coord.ReleaseWorkerView(t.Context(), "v-fb", nil) }()

	// 1. Usable fallback is reported as SeverityInfo with eager git init and copied entry/byte counts.
	receipt := BuildReceipt(snap)
	if receipt.AdapterID != adapter.AdapterEagerCopy ||
		receipt.Readiness != StateReady ||
		receipt.GitInit != GitInitInitialized ||
		receipt.FallbackReason != adapter.ReasonFUSEDeviceUnavailable ||
		receipt.Notice.Severity != SeverityInfo ||
		receipt.CopiedEntries == 0 || receipt.CopiedBytes == 0 {
		t.Fatalf("expected usable fallback receipt with SeverityInfo and initialized git, got %+v", receipt)
	}
	if len(cv.Metadata.Adapter.SeededGitRepos) == 0 {
		t.Fatalf("expected materialized claim to discover and initialize git repo during seed, got %+v", cv.Metadata.Adapter)
	}

	// 2. Live background command (CommandRunning) never makes a ready workspace appear unready.
	snapCmd, err := coord.SetCommandState("v-fb", CommandRunning)
	testutil.FailErr(t, "SetCommandState running", err)
	if snapCmd.Command != CommandRunning || snapCmd.Readiness != StateReady || snapCmd.Checkpoint.State != CheckpointNone {
		t.Fatalf("command running must keep readiness=ready and checkpoint distinct, got %+v", snapCmd)
	}

	// 3. Failed checkpoint persistence surfaces CheckpointFailedPersistence + ActionRetryCheckpoint while Readiness stays StateReady.
	snapCPFail, err := coord.RecordCheckpointFailure("v-fb", "cp-attempt-1", adapter.ReasonBarrierFailed, "disk full writing checkpoint")
	testutil.FailErr(t, "RecordCheckpointFailure", err)
	recCPFail := BuildReceipt(snapCPFail)
	if recCPFail.Readiness != StateReady ||
		recCPFail.Command != CommandRunning ||
		recCPFail.Checkpoint.State != CheckpointFailedPersistence ||
		recCPFail.Notice.Severity != SeverityWarning ||
		!slices.Contains(recCPFail.SupportedActions, ActionRetryCheckpoint) {
		t.Fatalf("failed checkpoint must remain distinct from readiness/command and offer retry_checkpoint, got %+v", recCPFail)
	}

	// 4. Subsequent checkpoint publication succeeds and updates LatestCheckpointID.
	snapCPPub, err := coord.RecordCheckpointPublished("v-fb", "cp-attempt-1")
	testutil.FailErr(t, "RecordCheckpointPublished", err)
	if snapCPPub.Checkpoint.State != CheckpointPublished || snapCPPub.Checkpoint.LatestCheckpointID != "cp-attempt-1" {
		t.Fatalf("expected published checkpoint state, got %+v", snapCPPub.Checkpoint)
	}
}

func TestSmallerWorkspaceSelectionRetryAfterLimitAndFocusHintsDoNotChangeWorkspaceContent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	rootSmall := filepath.Join(tmp, "root-small")
	rootDocs := filepath.Join(tmp, "root-docs")
	rootHuge := filepath.Join(tmp, "root-huge")
	for _, d := range []string{rootSmall, rootDocs, rootHuge} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}
	testutil.FailErr(t, "write small", os.WriteFile(filepath.Join(rootSmall, "app.go"), []byte("package app\n"), 0o644))
	testutil.FailErr(t, "write docs", os.WriteFile(filepath.Join(rootDocs, "README.md"), []byte("# docs\n"), 0o644))
	for i := 0; i < 25; i++ {
		testutil.FailErr(t, "write huge", os.WriteFile(filepath.Join(rootHuge, fmt.Sprintf("blob_%02d.bin", i)), []byte("0123456789"), 0o644))
	}

	// A disk with room for the small selection only.
	disk := func() (adapter.DiskSpace, error) {
		return adapter.DiskSpace{Path: tmp, Available: 64 * 1024, Known: true}, nil
	}
	sel := adapter.NewSelector("unknown-os", nil, nil, adapter.NewEagerCopyAdapter(adapter.ReasonUnsupportedPlatform))
	coord := NewCoordinator(sel)
	grants := []adapter.RootGrant{
		{RootID: "primary", Label: "primary", SourcePath: rootSmall, IsPrimary: true},
		{RootID: "docs", Label: "docs", SourcePath: rootDocs},
		{RootID: "huge", Label: "huge", SourcePath: rootHuge},
	}

	// 1. All 3 roots do not fit, which refuses with the number and offers a smaller selection.
	_, failSnap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity:        ClaimIdentity{WorkerID: "w-limit", ProjectID: "p", ViewID: "v-limit"},
		Storage:         ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:          grants,
		Materialization: ClaimMaterialization{Disk: disk},
	})
	var capErr *adapter.WorkspaceCapacityError
	if !errors.As(err, &capErr) || capErr.Available != 64*1024 || capErr.Required <= capErr.Available {
		t.Fatalf("expected a refusal with the numbers, got %v", err)
	}
	if failSnap.Readiness != StateFailedPreparation || failSnap.Notice.ReasonCode != adapter.ReasonWorkspaceDoesNotFit {
		t.Fatalf("expected StateFailedPreparation with ReasonWorkspaceDoesNotFit, got snap=%+v err=%v", failSnap, err)
	}
	if !slices.Contains(failSnap.SupportedActions, ActionSelectSmallerWorkspace) || !slices.Contains(failSnap.SupportedActions, ActionRetryClaim) {
		t.Fatalf("expected ActionSelectSmallerWorkspace and ActionRetryClaim, got %+v", failSnap.SupportedActions)
	}

	// 2. Caller selects a smaller permitted WorkspaceSelection (primary + docs only) and retries successfully.
	cv, okSnap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity:        ClaimIdentity{WorkerID: "w-limit", ProjectID: "p", ViewID: "v-limit"},
		Storage:         ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:          grants,
		Selection:       adapter.WorkspaceSelection{RootIDs: []string{"primary", "docs"}},
		FocusPathHints:  []string{"app.go"},
		Materialization: ClaimMaterialization{Disk: disk},
	})
	testutil.FailErr(t, "retry ClaimWorkerView with smaller selection", err)
	defer func() { _ = coord.ReleaseWorkerView(t.Context(), "v-limit", nil) }()

	if okSnap.Readiness != StateReady || len(okSnap.SelectedRoots) != 2 {
		t.Fatalf("expected ready 2-root workspace after retry, got %+v", okSnap)
	}
	rootsBefore := append([]RootMappingRecord(nil), okSnap.SelectedRoots...)

	// 3. Changing FocusPathHints alone never changes visible workspace roots or workspace content.
	updatedSnap, err := coord.UpdateFocusPathHints("v-limit", []string{"huge/blob_00.bin", "docs/README.md"})
	testutil.FailErr(t, "UpdateFocusPathHints", err)
	if !reflect.DeepEqual(updatedSnap.SelectedRoots, rootsBefore) {
		t.Fatalf("UpdateFocusPathHints must not alter SelectedRoots: before=%+v after=%+v", rootsBefore, updatedSnap.SelectedRoots)
	}
	if _, stErr := os.Stat(filepath.Join(cv.Prepared.RuntimeViewDir, "seeds", "huge")); !errors.Is(stErr, os.ErrNotExist) {
		t.Fatalf("changing focus hints must not materialize unselected root 'huge', got err=%v", stErr)
	}
}

func TestClaimInterruptedAtEachBoundaryLeavesNoReadyPartialWorkerOrLeakedRetention(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	testutil.FailErr(t, "mkdir src", os.MkdirAll(src, 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(src, "f.txt"), []byte("v1\n"), 0o644))

	coord := NewCoordinator(newMockMountedSelector(runtime.GOOS))
	ret := newTestRetentionRegistrar()
	grants := []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: src, IsPrimary: true}}

	_, _, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity: ClaimIdentity{WorkerID: "survivor", ProjectID: "p", ViewID: "view-survivor"},
		Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:   grants,
		Hooks:    ClaimHooks{Retention: ret, VerifyConfinement: passingConfinement},
	})
	testutil.FailErr(t, "survivor claim", err)
	defer func() { _ = coord.ReleaseWorkerView(context.Background(), "view-survivor", ret) }()

	stages := []ClaimStage{
		StageValidateSelection,
		StagePrepareAdapter,
		StageRegisterRetention,
		StageVerifyConfinement,
		StagePersistMetadata,
	}
	for _, stage := range stages {
		viewID := "view-fault-" + string(stage)
		metaPath := filepath.Join(tmp, "meta-"+string(stage)+".json")
		injected := errors.New("injected fault at " + string(stage))
		cv, snap, cErr := coord.ClaimWorkerView(t.Context(), ClaimRequest{
			Identity: ClaimIdentity{WorkerID: "w-" + string(stage), ProjectID: "p", ViewID: viewID},
			Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt"), MetadataPath: metaPath},
			Grants:   grants,
			Hooks: ClaimHooks{
				Retention:         ret,
				VerifyConfinement: passingConfinement,
				Fault: func(_ context.Context, s ClaimStage) error {
					if s == stage {
						return injected
					}
					return nil
				},
			},
		})
		if !errors.Is(cErr, injected) || cv != nil || snap.Readiness != StateFailedPreparation {
			t.Fatalf("stage %s: expected injected fault and StateFailedPreparation, got cv=%v snap=%+v err=%v", stage, cv, snap, cErr)
		}
		if ret.activeCountForView(viewID) != 0 {
			t.Fatalf("stage %s: leaked retention claim for %s", stage, viewID)
		}
		if _, stErr := os.Stat(metaPath); !errors.Is(stErr, os.ErrNotExist) {
			t.Fatalf("stage %s: expected no committed metadata file, got err=%v", stage, stErr)
		}
		if ret.activeCountForView("view-survivor") != 1 {
			t.Fatalf("stage %s: survivor retention claim was disturbed", stage)
		}
	}

	if _, _, cErr := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity: ClaimIdentity{WorkerID: "w-bad-conf", ProjectID: "p", ViewID: "view-bad-conf"},
		Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt")},
		Grants:   grants,
		Hooks: ClaimHooks{
			Retention: ret,
			VerifyConfinement: func(_ context.Context, _ *adapter.PreparedView) error {
				return errors.New("mount not usable inside sandbox")
			},
		},
	}); !errors.Is(cErr, ErrConfinementFailed) || ret.activeCountForView("view-bad-conf") != 0 {
		t.Fatalf("expected ErrConfinementFailed and 0 leaked retention claims, got %v", cErr)
	}
}
