package runstate

import (
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"path/filepath"
	"strings"
	"time"
)

// SetHostVar writes one host-managed workflow variable.

// approvalWrite applies one human_approval write and keeps awaiting_since on
// the moment the approval wait opened: stamped when the park begins, dropped
// when it ends, untouched by writes inside it.
func approvalWrite(vars map[string]any, write func(map[string]any) map[string]any) map[string]any {
	// Nested buckets are shared with the caller, so read the park state first.
	was := scaffoldvars.HumanApprovalAwaiting(vars)
	vars = write(vars)
	switch is := scaffoldvars.HumanApprovalAwaiting(vars); {
	case is && !was:
		vars = SetHostVar(vars, scaffoldvars.HumanApprovalAwaitingSincePath, time.Now().UTC().Format(time.RFC3339Nano))
	case !is && was:
		vars = CloneVars(vars)
		conditions.DeleteDotPath(vars, scaffoldvars.HumanApprovalAwaitingSincePath)
	}
	return vars
}

// SetHumanApprovalHash records the approved blueprint content hash.
func SetHumanApprovalHash(vars map[string]any, hash string) map[string]any {
	return approvalWrite(vars, func(vars map[string]any) map[string]any {
		return SetHostVar(vars, "human_approval.blueprint_hash", strings.TrimSpace(hash))
	})
}

// ClearHumanApprovalHash drops stored approval after blueprint mutation.
func ClearHumanApprovalHash(vars map[string]any) map[string]any {
	return approvalWrite(vars, func(vars map[string]any) map[string]any {
		vars = CloneVars(vars)
		conditions.DeleteDotPath(vars, "human_approval.blueprint_hash")
		return vars
	})
}

// SetHumanApprovalReady records whether approval prerequisites are satisfied.
func SetHumanApprovalReady(vars map[string]any, ready bool) map[string]any {
	return approvalWrite(vars, func(vars map[string]any) map[string]any {
		return SetHostVar(vars, "human_approval.ready", ready)
	})
}

// SetHumanApprovalIssued records explicit human approval.
func SetHumanApprovalIssued(vars map[string]any, issued bool) map[string]any {
	return approvalWrite(vars, func(vars map[string]any) map[string]any {
		return SetHostVar(vars, "human_approval.issued", issued)
	})
}

// SetHumanApprovalBlueprintPath binds approval to a blueprint path.
func SetHumanApprovalBlueprintPath(vars map[string]any, path string) map[string]any {
	return approvalWrite(vars, func(vars map[string]any) map[string]any {
		return SetHostVar(vars, "human_approval.blueprint_path", path)
	})
}

// StampHumanApprovalPhase records the active approval binding.
func StampHumanApprovalPhase(vars map[string]any, cfg *workflowdef.HumanApprovalConfig, runBlueprintPath string) map[string]any {
	if cfg == nil {
		return vars
	}
	path := filepath.ToSlash(strings.TrimSpace(runBlueprintPath))
	if path == "" {
		path = filepath.ToSlash(strings.TrimSpace(cfg.Blueprint))
	}
	vars = ClearHumanApprovalHash(vars)
	vars = approvalWrite(vars, func(vars map[string]any) map[string]any {
		return SetHostVar(vars, "human_approval.active", true)
	})
	if path == "" {
		return vars
	}
	return SetHumanApprovalBlueprintPath(vars, path)
}

const ObligationsVarKey = "obligations"
