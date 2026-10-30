package hitl_test

import (
 "reflect"
 "testing"

 "github.com/lycaon/lycaon/internal/gate"
 "github.com/lycaon/lycaon/internal/hitl"
 "github.com/lycaon/lycaon/internal/settings"
 "github.com/lycaon/lycaon/internal/testutil"
 "github.com/lycaon/lycaon/internal/toolcontract"
)

func directoryAuthorityOptions(t *testing.T) []hitl.ApprovalOption {
 t.Helper()
 target := gate.FileTarget{Path: "/fixture/user/go/pkg/mod/github.com/module", Mode: gate.ModeRead, OutsideRoots: true}
 _, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerFilePath, File: &target}, gate.PostureBalanced)
 action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "list_dir"}, Scope: hitl.ActionScope{SessionID: "session", ProjectID: "project", ProjectDir: "/fixture/project"}}
 options := []hitl.ApprovalOption{hitl.CurrentActionOption()}
 for _, offer := range settings.GrantedPathOffers(action, target, decision, nil) {
  options = append(options, hitl.GrantOption(offer))
 }
 if len(options) != 22 { t.Fatalf("directory ladder has %d options, want 22", len(options)) }
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
 if !reflect.DeepEqual(actual, options) { t.Fatal("restored directory authority changed") }
 releaseOptions()
 releaseRestored()
 if memory.InUse().State != 0 { t.Fatal("directory authority retained reservations") }
}
