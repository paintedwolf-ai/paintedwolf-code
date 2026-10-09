package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMergeStartParamsRequestOverridesDefaultsAndPreset(t *testing.T) {
	manifest := workflowdef.Manifest{
		Parameters: map[string]workflowdef.WorkflowParameter{
			"research_depth": {Type: "depth", Default: "light"},
			"auto_approve":   {Type: "boolean", Default: "false"},
		},
		Presets: []workflowdef.ManifestPreset{{
			ID: "quick",
			Params: map[string]string{
				"research_depth": "thorough",
			},
		}},
	}
	got, err := workflowdef.MergeStartParams(manifest, "quick", api.StartWorkflowRunRequest{
		Parameters: map[string]string{"research_depth": " NONE "},
	})
	if err != nil {
		t.Fatalf("MergeStartParams: %v", err)
	}
	if got["research_depth"] != "none" || got["auto_approve"] != "false" {
		t.Fatalf("params = %#v", got)
	}
}

func TestStartHumanAppliesRequestParameters(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	run, err := mgr.Starts.StartHuman(context.Background(), "sess-1", api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
		Parameters:      map[string]string{"research_depth": "none"},
	})
	testutil.FailErr(t, "start workflow with parameters", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(context.Background(), run.ID)
	testutil.FailErr(t, "load workflow vars", err)
	if got, _ := runstate.DotPathString(vars, "params.research_depth"); got != "none" {
		t.Fatalf("params.research_depth = %q, want none", got)
	}
	if !conditions.DotPathTruthy(vars, "phase_skipped.research") {
		t.Fatal("research_depth=none did not resolve the research phase")
	}
}

func TestInvalidStartParameterDoesNotCreateBlueprint(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	before, _, err := blueprintMgr.List(ctx, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list blueprints before invalid start", err)
	_, err = mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
		Parameters:      map[string]string{"research_depth": "impossible"},
	})
	if !errors.Is(err, workflowdef.ErrWorkflowParameterInvalid) {
		t.Fatalf("start err = %v, want ErrWorkflowParameterInvalid", err)
	}
	after, _, err := blueprintMgr.List(ctx, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list blueprints after invalid start", err)
	if len(after) != len(before) {
		t.Fatalf("invalid start created a blueprint: before=%d after=%d", len(before), len(after))
	}
}

func TestMergeStartParamsRejectsUnknownAndInvalidValues(t *testing.T) {
	manifest := workflowdef.Manifest{Parameters: map[string]workflowdef.WorkflowParameter{
		"research_depth": {Type: "depth", Default: "light"},
	}}
	for name, params := range map[string]map[string]string{
		"unknown": {"other": "none"},
		"invalid": {"research_depth": "huge"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := workflowdef.MergeStartParams(manifest, "", api.StartWorkflowRunRequest{Parameters: params})
			if !errors.Is(err, workflowdef.ErrWorkflowParameterInvalid) {
				t.Fatalf("err = %v, want ErrWorkflowParameterInvalid", err)
			}
		})
	}
	_, err := workflowdef.MergeStartParams(manifest, "missing", api.StartWorkflowRunRequest{})
	if !errors.Is(err, workflowdef.ErrWorkflowParameterInvalid) {
		t.Fatalf("unknown preset err = %v, want ErrWorkflowParameterInvalid", err)
	}
}
