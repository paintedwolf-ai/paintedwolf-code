package tools

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
)

// ApprovalPolicyEngine evaluates sandbox profiles then user approval rules.
type ApprovalPolicyEngine struct {
	profile *ProfilePolicyEngine
	gate    hitl.ApprovalGate
}

// NewApprovalPolicyEngine constructs a policy engine with profile and approval checks.
func NewApprovalPolicyEngine(profile *ProfilePolicyEngine, gate hitl.ApprovalGate) *ApprovalPolicyEngine {
	return &ApprovalPolicyEngine{profile: profile, gate: gate}
}

func (p *ApprovalPolicyEngine) WaitConditions(profileID string) []string {
	if p == nil || p.profile == nil {
		return nil
	}
	return p.profile.WaitConditions(profileID)
}

// EvaluateForList returns tools allowed by the sandbox profile only (not approval ask/deny).
func (p *ApprovalPolicyEngine) EvaluateForList(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	if p == nil || p.profile == nil {
		return &platform.PolicyDecision{Allowed: true}, nil
	}
	return p.profile.Evaluate(ctx, eval)
}

// Evaluate checks profile access then approval rules.
func (p *ApprovalPolicyEngine) Evaluate(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	decision, err := p.profile.Evaluate(ctx, eval)
	if err != nil {
		return nil, err
	}
	if decision.Blocked {
		return decision, nil
	}
	if eval.BoundaryApprovalSatisfied && (len(eval.ConfineRequest.PolicyWriteGrants) == 0 || eval.PolicyWritesReviewed) {
		return decision, nil
	}
	if p.gate == nil {
		return decision, nil
	}

	action := proposedActionFromPolicy(eval)
	approval, err := p.gate.Evaluate(ctx, action)
	if err != nil {
		return nil, err
	}
	if approval.Denied {
		return &platform.PolicyDecision{
			Blocked:     true,
			RejectCode:  approval.DenyCode,
			RejectData:  denyRejectData(eval, approval),
			BlockReason: approval.DenyCode,
			Approval:    approval,
		}, nil
	}
	if approval.Required() {
		return &platform.PolicyDecision{
			Allowed:            false,
			RequiresApproval:   true,
			ApprovalActionType: eval.ToolName,
			Approval:           approval,
		}, nil
	}
	return &platform.PolicyDecision{Allowed: true}, nil
}

// pathArgKeys are the scalar/list argument names that name a file a tool will touch.
var pathArgKeys = []string{"path", "paths", "dest"}

// pairArgKeys identify batch file-operation arguments.
var pairArgKeys = []string{"copies", "moves"}

// pairPathKeys are the canonical endpoints inside a pair.
var pairPathKeys = []string{"from", "to"}

// commandStreamTools bind stream files through a command plan.
var commandStreamTools = map[string]bool{"command": true, "verify": true, "terminal_open": true}

// Path rules include scalar, list, and batch endpoints, and every file a
// command plan streams to or from.
func filesFromArgs(tool string, args map[string]any) []string {
	if args == nil {
		return nil
	}
	if commandStreamTools[tool] {
		return commandStreamFiles(args)
	}
	var files []string
	appendString := func(v any) {
		if s, ok := v.(string); ok && s != "" {
			files = append(files, s)
		}
	}
	for _, key := range pathArgKeys {
		v, ok := args[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			appendString(t)
		case []any:
			for _, item := range t {
				appendString(item)
			}
		case []string:
			files = append(files, t...)
		}
	}
	files = append(files, pairFilesFromArgs(args)...)
	return files
}

// commandStreamFiles reads redirection and stream-field paths from the plan.
// A plan that does not parse cannot run; its explicit fields still name files.
func commandStreamFiles(args map[string]any) []string {
	plan, err := commandsurface.ParsePlan(args)
	if err == nil {
		return append(plan.WritePaths(), plan.ReadPaths()...)
	}
	var files []string
	for _, key := range []string{"stdout_to", "stderr_to", "stdin_from"} {
		if s, ok := args[key].(string); ok && strings.TrimSpace(s) != "" {
			files = append(files, strings.TrimSpace(s))
		}
	}
	return files
}

// pairFilesFromArgs pulls both endpoints out of a batch-of-pairs argument.
func pairFilesFromArgs(args map[string]any) []string {
	var files []string
	for _, key := range pairArgKeys {
		items, ok := args[key].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			pair, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, field := range pairPathKeys {
				if s, ok := pair[field].(string); ok && s != "" {
					files = append(files, s)
				}
			}
		}
	}
	return files
}

// proposedActionFromPolicy builds the gate input, stamping Contained from the
// same confine request the executor's spawn path will apply.
func proposedActionFromPolicy(eval platform.PolicyContext) hitl.ProposedAction {
	confReq := eval.ConfineRequest
	if len(confReq.Roots) == 0 && strings.TrimSpace(eval.ProjectDir) != "" {
		confReq.Roots = []string{eval.ProjectDir}
	}
	if (eval.DirectIPRequested || eval.AuthorizedDirectIP) && confReq.Egress != confine.EgressDirectIP {
		confReq.Egress = confine.EgressDirectIP
		confReq.SocksProxyEnv = false
	}
	return hitl.ProposedAction{
		AgentPolicy:             policyWriteTargets(confReq, eval.ProjectDir),
		Tool:                    eval.ToolName,
		Args:                    eval.ToolArgs,
		ApprovalCategory:        eval.ApprovalCategory,
		ApprovalSubject:         eval.ApprovalSubject,
		Files:                   append(filesFromArgs(eval.ToolName, eval.ToolArgs), policyWritePaths(confReq)...),
		ResolvedFiles:           append(append([]string(nil), eval.ResolvedFiles...), policyWritePaths(confReq)...),
		HostResources:           append([]string(nil), eval.HostResources...),
		HostResourceFamilies:    append([]string(nil), eval.HostResourceFamilies...),
		ProjectID:               eval.ProjectID,
		ProjectDir:              eval.ProjectDir,
		SessionID:               eval.SessionID,
		RootSessionID:           eval.ChatSessionID(),
		SessionScratchRoot:      confReq.SessionScratchRoot,
		Contained:               hitl.ContainedForRequest(confReq),
		SocketGrants:            append([]confine.SocketGrant(nil), eval.SocketGrants...),
		SocketScopes:            append([]string(nil), eval.SocketScopes...),
		SocketGrantStates:       append([]string(nil), eval.SocketGrantStates...),
		AuthorizedSocketDigests: append([]string(nil), eval.AuthorizedSocketDigests...),
		AuthorizedDirectIP:      eval.AuthorizedDirectIP,
		ActionID:                eval.ActionID,
		DirectIPRequested:       eval.DirectIPRequested,
		Visibility:              eval.DirectIPVisibility,
		DeclaredDestinations:    append([]string(nil), eval.DeclaredDestinations...),
		PackageExecution:        eval.PackageExecution,
	}
}

// denyRejectData is the denied path and first matched rule plus tool and profile.
func denyRejectData(eval platform.PolicyContext, approval *hitl.ApprovalResult) map[string]any {
	data := map[string]any{"tool": eval.ToolName, "profile": eval.ProfileID}
	if approval == nil {
		return data
	}
	if subject := strings.TrimSpace(approval.DenySubject); subject != "" {
		data["path"] = subject
	}
	if len(approval.MatchedRules) == 0 {
		return data
	}
	primary := approval.MatchedRules[0]
	data["rule_unit_id"] = primary.UnitID
	data["rule_pack_id"] = primary.PackID
	data["rule_scope"] = primary.Scope
	data["rule_category"] = primary.Category
	data["rule_pattern"] = primary.Pattern
	if primary.Command != "" {
		data["command"] = primary.Command
	}
	data["rule_match_count"] = strconv.Itoa(len(approval.MatchedRules))
	return data
}
