package toolexecution_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/query"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// Protected command writes wait for approval before publishing bytes.
func TestCommandJqCredentialDestinationWaitsForApproval(t *testing.T) {
	f := programtest.New(t, programtest.Options{Tools: []string{"jq"}})
	project := f.Dir
	credential := filepath.Join(project, ".env")
	const original = "{\"port\": 8080}\n"
	testutil.FailErr(t, "seed credential document", os.WriteFile(credential, []byte(original), 0600))
	testutil.FailErr(t, "seed query source", os.WriteFile(filepath.Join(project, "config.json"), []byte(original), 0600))
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := f.Boundary
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), f.Runtime.Registry, "fixture")
	f.Runtime.Executor = executor
	owners, err := query.NewOwners(native.ExecutionRevisions{Boundary: boundary}, f.Recorder, &native.WriteTool{Boundary: boundary})
	testutil.FailErr(t, "query owners", err)
	mutations, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: boundary}, f.Recorder, mutation.Operations{
		Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary}, Move: &native.MoveTool{Boundary: boundary}, Copy: &native.CopyTool{Boundary: boundary}, Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary}, Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary}, Extract: &native.ExtractArchiveTool{Boundary: boundary},
	})
	testutil.FailErr(t, "publication owner", err)
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		scoped := *owners
		scoped.Revisions.Memory = memory
		scopedMutations := *mutations
		scopedMutations.Revisions.Memory, scopedMutations.Admission = memory, admission
		publication := &mutation.Publication{Owners: &scopedMutations, Program: mutation.Program{Paths: paths}, Tools: tc}
		return command.ProgramOwners{Input: publication, Publication: func(admit tools.CommandOutputAdmission) commandprogram.OutputPublication {
			copy := *publication
			copy.Admission = admit
			return &copy
		}, Natives: map[string]command.NativeOperation{
			"jq": {Tool: "jq", Prepare: func(_ context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
				prepared, err := scoped.Prepare(query.Program{Paths: paths}, request)
				return command.NativePrepared{Tool: prepared.Operation, Review: prepared.Review, Invoke: prepared.InvokeOwner}, err
			}},
		}}, nil
	}
	manager := &asyncHITL{requested: make(chan struct{}, 1)}
	executor.Approvals.SetCheckpointManager(t.Context(), manager, approvalGate)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		raw, err := f.Call(t, ctx, `jq '.port = 9090' config.json > .env`)
		if err == nil {
			var outcome command.ProgramOutcome
			err = json.Unmarshal([]byte(raw), &outcome)
			if err == nil && outcome.Status != 0 {
				err = fmt.Errorf("command status %d: %s", outcome.Status, raw)
			}
		}
		done <- err
	}()

	select {
	case <-manager.requested:
	case err := <-done:
		t.Fatalf("jq output to .env finished without asking: %v", err)
	case <-ctx.Done():
		t.Fatal("jq output to .env did not ask")
	}
	pending, err := os.ReadFile(credential)
	testutil.FailErr(t, "read pending credential", err)
	if string(pending) != original {
		t.Fatal("credential changed before approval")
	}

	request := manager.request
	if request.Kind != api.CheckpointKindToolApproval || !slices.Contains(request.Decision.Gates(), api.GateSensitiveLocation) {
		t.Fatalf("jq output to .env asked %s with gates %v, want a tool approval citing %s",
			request.Kind, request.Decision.Gates(), api.GateSensitiveLocation)
	}

	manager.approve()
	testutil.FailErr(t, "complete the approved write", <-done)
	body, err := os.ReadFile(credential)
	testutil.FailErr(t, "read the credential file", err)
	var landed struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(body, &landed); err != nil || landed.Port != 9090 {
		t.Fatalf("approved bytes did not land: %q (%v)", body, err)
	}
}
