package authzcontext

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// EventDetail is the redacted detail_json payload.
type EventDetail struct {
	ToolCallID          string                 `json:"tool_call_id,omitempty"`
	ToolName            string                 `json:"tool_name,omitempty"`
	RejectCode          string                 `json:"reject_code,omitempty"`
	Tier                string                 `json:"tier,omitempty"`
	Path                string                 `json:"path,omitempty"`
	GrantScope          string                 `json:"grant_scope,omitempty"`
	RawArgv             string                 `json:"raw_argv,omitempty"`
	AuthorizationSource string                 `json:"authorization_source,omitempty"`
	SuppressionCause    string                 `json:"suppression_cause,omitempty"`
	AskFamily           string                 `json:"ask_family,omitempty"`
	ContentDecision     string                 `json:"content_decision,omitempty"`
	BlueprintDigest     string                 `json:"blueprint_digest,omitempty"`
	CheckpointID        string                 `json:"checkpoint_id,omitempty"`
	PlanID              string                 `json:"plan_id,omitempty"`
	ActionDigest        string                 `json:"action_digest,omitempty"`
	SelectedOptionID    string                 `json:"selected_option_id,omitempty"`
	GrantIDs            []string               `json:"grant_ids,omitempty"`
	SubjectKind         string                 `json:"subject_kind,omitempty"`
	SubjectTitle        string                 `json:"subject_title,omitempty"`
	Gate                string                 `json:"gate,omitempty"`
	Reasons             []string               `json:"reasons,omitempty"`
	ExternalAccess      *api.ExternalAccess    `json:"external_access,omitempty"`
	ApprovalRules       []ApprovalRuleCitation `json:"approval_rules,omitempty"`
	ResolverPolicy      *ResolverPolicy        `json:"resolver_policy,omitempty"`
}

// ResolverPolicy names the standing policy rule that settled a policy-resolved decision.
type ResolverPolicy struct {
	PackID string `json:"pack_id"`
	UnitID string `json:"unit_id"`
	RuleID string `json:"rule_id"`
}

type ApprovalRuleCitation struct {
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
	Effect   string `json:"effect"`
	UnitID   string `json:"unit_id,omitempty"`
	PackID   string `json:"pack_id,omitempty"`
	Scope    string `json:"scope,omitempty"`
}

// DetailInput collects machine state for detail_json assembly.
type DetailInput struct {
	ToolCallID          string
	Tool                string
	Args                map[string]any
	Files               []string
	ProjectDir          string
	Tier                string
	RejectCode          string
	GrantScope          GrantScope
	CaptureArgv         bool
	AuthorizationSource string
	SuppressionCause    string
	AskFamily           string
	ContentDecision     string
	BlueprintDigest     string
	CheckpointID        string
	PlanID              string
	ActionDigest        string
	SelectedOptionID    string
	GrantIDs            []string
	SubjectKind         string
	SubjectTitle        string
	Gate                string
	Reasons             []string
	ExternalAccess      *api.ExternalAccess
	ExternalAccessInput *ExternalAccessInput
	ApprovalRules       []authzledger.ApprovalRuleCitation
	ResolverPolicy      *authzledger.PolicyIdentity
}

// BuildEventDetail assembles a redacted detail payload from machine state only.
func BuildEventDetail(in DetailInput) EventDetail {
	d := EventDetail{
		ToolCallID:          strings.TrimSpace(in.ToolCallID),
		ToolName:            strings.TrimSpace(in.Tool),
		RejectCode:          strings.TrimSpace(in.RejectCode),
		Tier:                strings.TrimSpace(in.Tier),
		AuthorizationSource: strings.TrimSpace(in.AuthorizationSource),
		SuppressionCause:    strings.TrimSpace(in.SuppressionCause),
		AskFamily:           strings.TrimSpace(in.AskFamily),
		ContentDecision:     strings.TrimSpace(in.ContentDecision),
		BlueprintDigest:     strings.TrimSpace(in.BlueprintDigest),
		CheckpointID:        strings.TrimSpace(in.CheckpointID),
		PlanID:              strings.TrimSpace(in.PlanID),
		ActionDigest:        strings.TrimSpace(in.ActionDigest),
		SelectedOptionID:    strings.TrimSpace(in.SelectedOptionID),
		GrantIDs:            append([]string(nil), in.GrantIDs...),
		SubjectKind:         strings.TrimSpace(in.SubjectKind),
		SubjectTitle:        strings.TrimSpace(in.SubjectTitle),
		Gate:                strings.TrimSpace(in.Gate),
		Reasons:             append([]string(nil), in.Reasons...),
	}
	for _, rule := range in.ApprovalRules {
		d.ApprovalRules = append(d.ApprovalRules, ApprovalRuleCitation{
			Category: rule.Category, Pattern: rule.Pattern, Effect: rule.Effect,
			UnitID: rule.UnitID, PackID: rule.PackID, Scope: rule.Scope,
		})
	}
	if policy := in.ResolverPolicy; policy != nil {
		d.ResolverPolicy = &ResolverPolicy{
			PackID: strings.TrimSpace(policy.PackID), UnitID: strings.TrimSpace(policy.UnitID), RuleID: strings.TrimSpace(policy.RuleID),
		}
	}
	if scope := strings.TrimSpace(string(in.GrantScope)); scope != "" {
		d.GrantScope = scope
	}
	if rel := workspaceRelativePath(in.ProjectDir, in.Files); rel != "" {
		d.Path = rel
	}
	if in.CaptureArgv {
		if cmd := commandsurface.PrimaryCommandLine(in.Args, nil); cmd != "" {
			d.RawArgv = cmd
		}
	}
	switch {
	case in.ExternalAccess != nil:
		d.ExternalAccess = in.ExternalAccess
	case in.ExternalAccessInput != nil:
		d.ExternalAccess = BuildExternalAccess(*in.ExternalAccessInput)
	}
	return d
}

func workspaceRelativePath(projectDir string, files []string) string {
	if len(files) == 0 {
		return ""
	}
	p := strings.TrimSpace(files[0])
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return p
	}
	rel, err := filepath.Rel(projectDir, filepath.FromSlash(p))
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}
