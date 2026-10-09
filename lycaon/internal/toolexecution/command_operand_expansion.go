package toolexecution

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// expandCommandOperands resolves unquoted @scratch arguments to their paths in
// the session's scratch folder, then expands unquoted glob patterns within the
// working directory. The new arguments match the call's pre-grant read
// boundaries, and the call keeps the arguments as written for display.
func (e *Boundary) expandCommandOperands(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	tc *tools.ToolContext,
) (map[string]any, error) {
	if tool != "command" && tool != "verify" {
		return args, nil
	}
	// A plan that does not parse has nothing to expand: the handler refuses
	// that call with its own structured reason.
	if plan, err := commandsurface.ParsePlan(args); err == nil {
		return e.expandPlanOperands(ctx, tool, profileID, args, plan, tc)
	}
	return args, nil
}

func (e *Boundary) expandPlanOperands(
	ctx context.Context,
	tool, profileID string,
	args map[string]any,
	plan commandsurface.Plan,
	tc *tools.ToolContext,
) (map[string]any, error) {
	stages, addressed, err := commandsurface.ResolveScratchOperands(plan.Stages, tc.SessionScratchDir)
	if err != nil {
		if reject := scratchOperandReject(err); reject != nil {
			return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
		}
		return nil, err
	}
	expanded := false
	if dir, ok := commandDir(plan, *tc); ok {
		confReq := e.actionConfineRequest(ctx, *tc)
		stages, expanded, err = commandsurface.ExpandGlobs(ctx, stages, commandsurface.GlobScope{
			Dir: dir, Readable: globReadable(confReq),
		})
		if err != nil {
			if reject := toolcommand.ArgvShapeObservation(tool, err); reject != nil {
				return nil, e.Rejections.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
			}
			return nil, err
		}
	}
	if !addressed && !expanded {
		return args, nil
	}
	tc.RequestedArgs = args
	return withStages(args, stages), nil
}

// commandDir resolves the directory a plan runs in. A cwd that does not
// resolve has nothing to expand against: the handler refuses it.
func commandDir(plan commandsurface.Plan, tc tools.ToolContext) (string, bool) {
	cwd := plan.IO.Cwd
	if cwd == "" {
		cwd = "."
	}
	dir, err := tools.ApprovalFilePath(tc, cwd)
	return dir, err == nil
}

// withStages returns args carrying stages in the argument form they came in.
func withStages(args map[string]any, stages []exec.Stage) map[string]any {
	out := maps.Clone(args)
	if command, _ := args["command"].(string); strings.TrimSpace(command) != "" {
		out["command"] = commandsurface.RenderStages(stages)
		return out
	}
	lines := make([]any, len(stages))
	for i := range stages {
		lines[i] = stages[i].CommandLine()
	}
	out["pipeline"] = lines
	return out
}

// scratchOperandReject names an @scratch argument the model can correct; any
// other failure is the host's.
func scratchOperandReject(err error) *toolrejection.ToolReject {
	operand := ""
	var operandErr *commandsurface.ScratchOperandError
	if errors.As(err, &operandErr) {
		operand = operandErr.Operand
	}
	if errors.Is(err, commandsurface.ErrScratchUnavailable) {
		return &toolrejection.ToolReject{Code: tools.SessionScratchUnavailableCode, Data: map[string]any{"path": operand}}
	}
	if errors.Is(err, projectroot.ErrPathEscape) {
		return &toolrejection.ToolReject{Code: "SURVEY_PATH_ESCAPE", Data: map[string]any{"path": operand, "reason": err.Error()}}
	}
	return nil
}

// globReadable is the read verdict of the boundary the command launches under.
func globReadable(confReq confine.Request) func(string) bool {
	confinement, _ := confine.DefaultConfinement(confReq)
	rules := confine.BoundaryOf(confinement).Filesystem
	return func(abs string) bool {
		return !confine.ControlPlanePathDenied(abs, false, confReq.SessionScratchRoot) &&
			rules.Verdict(confine.AccessRead, abs).Allowed
	}
}
