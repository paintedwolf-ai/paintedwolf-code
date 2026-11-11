package hitl_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func directoryAuthorityOptions(t *testing.T) []hitl.ApprovalOption {
	t.Helper()
	target := directoryAuthorityTarget(t)
	_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerFilePath, File: &target}, gate.PostureBalanced)
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "list_dir"}, Scope: hitl.ActionScope{SessionID: "session", ProjectID: "project", ProjectDir: t.TempDir()}}
	options := []hitl.ApprovalOption{hitl.CurrentActionOption()}
	for _, offer := range settings.GrantedPathOffers(action, target, decision, nil) {
		options = append(options, hitl.GrantOption(offer))
	}
	if len(options) != 22 {
		t.Fatalf("directory ladder has %d options, want 22", len(options))
	}
	return options
}

func TestDirectoryAuthorityFitsProductionCapacity(t *testing.T) {
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "read production memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create production memory ledger", err)
	options := directoryAuthorityOptions(t)
	projection, release, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, options)
	testutil.FailErr(t, "encode directory authority", err)
	release()
	restored, releaseRestored, err := hitl.RestoreNativeAuthorityProjection(t.Context(), memory, projection.Representation(), projection.Digest(), projection.Canonical())
	testutil.FailErr(t, "restore directory authority", err)
	actual, releaseOptions, err := restored.Options(t.Context(), memory)
	testutil.FailErr(t, "read restored directory choices", err)
	if !reflect.DeepEqual(actual, options) {
		t.Fatal("restored directory authority changed")
	}
	releaseOptions()
	releaseRestored()
	if memory.InUse().State != 0 {
		t.Fatal("directory authority retained reservations")
	}
}

func productionAuthorityMemory(t *testing.T) *toolcontract.ProgramMemory {
	t.Helper()
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "read authority limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create authority memory", err)
	return memory
}

func mintDirectoryApproval(t *testing.T, host nativeAuthorityHost, memory *toolcontract.ProgramMemory) (context.Context, string, *hitl.ApprovalPlan) {
	t.Helper()
	const sessionID = "session"
	insertSession(t, host.database, sessionID)
	ctx := testdbseed.OwnerCaller(t, t.Context(), host.database)
	host.manager.Authority.SetNativeAuthorityAdmission(memory)
	review := hitl.NewNativeActionReview(ctx, memory, nativeInspectionFixture{})
	defer review.Close()
	target := directoryAuthorityTarget(t)
	args, native, err := review.Freeze(ctx, "list_dir", map[string]any{"path": filepath.Dir(target.Path) + string(filepath.Separator)})
	testutil.FailErr(t, "freeze directory request", err)
	action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "list_dir", Args: args, NativeInputs: native, ActionID: "call-directory"}, Scope: hitl.ActionScope{SessionID: sessionID, ProjectID: testdbseed.DefaultProjectID, ProjectDir: t.TempDir()}}
	_, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerFilePath, File: &target}, gate.PostureBalanced)
	response, err := requestExplicitApprovalCheckpoint(t, ctx, host.manager, hitl.CheckpointRequest{SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ToolCallID: "call-directory", ProposedAction: &action, GrantOffers: settings.GrantedPathOffers(action, target, decision, nil)})
	testutil.FailErr(t, "publish directory approval", err)
	row, err := host.manager.Store.Get(ctx, response.CheckpointID)
	testutil.FailErr(t, "read directory card", err)
	raw, err := json.Marshal(row.Payload["approval_plan"])
	testutil.FailErr(t, "encode displayed directory plan", err)
	var plan hitl.ApprovalPlan
	testutil.FailErr(t, "read displayed directory plan", json.Unmarshal(raw, &plan))
	if len(plan.Options) != 22 {
		t.Fatalf("published %d directory choices, want 22", len(plan.Options))
	}
	return ctx, response.CheckpointID, &plan
}

func TestEveryDirectoryChoiceResolvesAfterRestartWithinProductionCapacity(t *testing.T) {
	for index := range 22 {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			memory := productionAuthorityMemory(t)
			minting := startNativeAuthorityHost(t, path)
			ctx, checkpointID, plan := mintDirectoryApproval(t, minting, memory)
			if memory.InUse().State != 0 {
				t.Fatal("publishing retained approval scratch")
			}
			resolving := startNativeAuthorityHost(t, path)
			resolving.manager.Authority.SetNativeAuthorityAdmission(memory)
			installer := &nativeInstallRecorder{}
			resolving.manager.Authority.SetApprovalAuthorityInstaller(installer)
			chosen := plan.Options[index]
			_, err := resolving.manager.Authority.ResolveApprovalOption(ctx, "session", checkpointID, chosen.ID)
			testutil.FailErr(t, "answer persisted directory choice", err)
			row, err := resolving.manager.Store.Get(ctx, checkpointID)
			testutil.FailErr(t, "read resolved directory choice", err)
			if row.Status != hitl.DecisionStatusApproved || row.Result.OptionID != chosen.ID || len(installer.installed) != 1 {
				t.Fatal("directory decision did not commit exactly once")
			}
			if !reflect.DeepEqual(installer.installed[0].Authority[0].GrantedPath, chosen.Authority[0].GrantedPath) {
				t.Fatal("installed directory scope changed")
			}
			_, err = resolving.manager.Authority.ResolveApprovalOption(ctx, "session", checkpointID, chosen.ID)
			testutil.FailErr(t, "repeat committed decision", err)
			if len(installer.installed) != 1 || memory.InUse().State != 0 {
				t.Fatal("decision retry installed twice or retained scratch")
			}
			if memory.Peak().State > 16<<20 {
				t.Fatalf("directory lifecycle reserved %d bytes, want at most 16 MiB", memory.Peak().State)
			}
		})
	}
}

func TestDirectoryCapacityFailureKeepsRejectionAvailable(t *testing.T) {
	host := startNativeAuthorityHost(t, filepath.Join(t.TempDir(), "store.db"))
	memory := productionAuthorityMemory(t)
	ctx, id, plan := mintDirectoryApproval(t, host, memory)
	installer := &nativeInstallRecorder{}
	host.manager.Authority.SetApprovalAuthorityInstaller(installer)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "read decision capacity", err)
	release, err := memory.ReserveProgramState(ctx, limits.State)
	testutil.FailErr(t, "occupy decision capacity", err)
	for range 3 {
		_, err := host.manager.Authority.ResolveApprovalOption(ctx, "session", id, plan.Options[0].ID)
		var capacity *toolcontract.ProgramMemoryLimitError
		if !errors.As(err, &capacity) {
			t.Fatalf("expected typed capacity refusal, got %v", err)
		}
		if memory.InUse().State != limits.State || len(installer.installed) != 0 {
			t.Fatal("failed approval leaked or installed authority")
		}
	}
	_, err = host.manager.ResolveCheckpoint(ctx, "session", id, api.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "reject without restoring grant authority", err)
	release()
	if memory.InUse().State != 0 {
		t.Fatal("rejection retained approval scratch")
	}
}

func directoryAuthorityTarget(t *testing.T) gate.FileTarget {
	t.Helper()
	scratch := t.TempDir()
	root := filepath.VolumeName(scratch) + string(filepath.Separator)
	folder := "paintedwolf-approval-fixture-" + filepath.Base(filepath.Dir(scratch))
	return gate.FileTarget{Path: filepath.Join(root, folder, "user", "go", "pkg", "mod", "github.com", "module"), Mode: gate.ModeRead, OutsideRoots: true}
}
