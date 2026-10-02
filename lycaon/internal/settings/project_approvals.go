package settings

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// Reasons a repository approvals.yaml entry was not applied. They share the
// detection-pack and scanner overlay vocabulary.
const (
	ProjectApprovalsUnreadable      = "project_unreadable"
	ProjectApprovalsFieldsForbidden = "project_fields_forbidden"
	ProjectApprovalsInvalidEntry    = "invalid_entry"
)

// ErrProjectApprovalsNeedRepair refuses a host write that would replace
// repository approval content the host could not apply.
var ErrProjectApprovalsNeedRepair = errors.New("project approvals.yaml has entries that were not applied; edit the file before saving")

// ProjectApprovalRejection is one part of a repository approvals.yaml the host
// did not apply.
type ProjectApprovalRejection struct {
	// Entry locates the refused part (approval_posture, rules[2], grants,
	// never_ask). Empty means the whole file.
	Entry  string
	Code   string
	Detail string
}

// ProjectApprovals is a repository approvals.yaml as the merge applies it.
type ProjectApprovals struct {
	Config   ApprovalConfig
	Rejected []ProjectApprovalRejection
}

// projectApprovalsFile is the repository document. The posture stays a token
// so a typo refuses that entry rather than the rules beside it, and grants are
// read only to report them.
type projectApprovalsFile struct {
	Rules       []ApprovalRule `yaml:"rules"`
	Grants      yaml.Node      `yaml:"grants"`
	Posture     string         `yaml:"approval_posture"`
	AIRationale *bool          `yaml:"ai_rationale"`
	NeverAsk    *bool          `yaml:"never_ask"`
}

func projectApprovalsRel() string {
	return settingsoverlay.Rel(settingsoverlay.BasenameApprovals)
}

// readProjectApprovals reads the file on every call, so an outside edit
// applies to the next decision. Repository policy can only tighten, so what
// cannot be applied fails closed: an unreadable file or posture reads as
// Strict with approvals on. Every refusal is reported.
func readProjectApprovals(projectDir string) ProjectApprovals {
	data, err := os.ReadFile(projectApprovalsPath(projectDir)) // #nosec G304 -- project overlay under the caller's project dir
	if errors.Is(err, fs.ErrNotExist) {
		return ProjectApprovals{}
	}
	if err != nil {
		return unreadableProjectApprovals(fmt.Sprintf("could not read %s: %v", projectApprovalsRel(), err))
	}
	var file projectApprovalsFile
	if err := config.DecodeYAML(data, &file); err != nil {
		if errors.Is(err, io.EOF) {
			return ProjectApprovals{}
		}
		return unreadableProjectApprovals(fmt.Sprintf("could not parse %s: %v", projectApprovalsRel(), err))
	}
	return applyProjectApprovalsFile(file)
}

func unreadableProjectApprovals(detail string) ProjectApprovals {
	approvalsOn := false
	return ProjectApprovals{
		Config:   ApprovalConfig{Posture: gate.PostureStrict, NeverAsk: &approvalsOn},
		Rejected: []ProjectApprovalRejection{{Code: ProjectApprovalsUnreadable, Detail: detail}},
	}
}

func applyProjectApprovalsFile(file projectApprovalsFile) ProjectApprovals {
	var out ProjectApprovals
	refuse := func(entry, code, detail string) {
		out.Rejected = append(out.Rejected, ProjectApprovalRejection{Entry: entry, Code: code, Detail: detail})
	}
	for i, rule := range file.Rules {
		entry := fmt.Sprintf("rules[%d]", i)
		rule = trimApprovalRule(rule)
		if rule.Effect != "" && !allowedEffects[rule.Effect] {
			refuse(entry, ProjectApprovalsFieldsForbidden, fmt.Sprintf("a project rule can ask or deny, not %q", rule.Effect))
			continue
		}
		if err := ValidatePolicyRules([]ApprovalRule{rule}); err != nil {
			refuse(entry, ProjectApprovalsInvalidEntry, err.Error())
			continue
		}
		out.Config.Rules = append(out.Config.Rules, rule)
	}
	if file.Posture != "" {
		posture, err := gate.ParsePosture(file.Posture)
		if err != nil {
			posture = gate.PostureStrict
			refuse("approval_posture", ProjectApprovalsInvalidEntry, err.Error()+"; this project uses strict until it is fixed")
		}
		out.Config.Posture = posture
	}
	if file.Grants.Kind != 0 {
		refuse("grants", ProjectApprovalsFieldsForbidden, "saved approvals belong to this device and cannot come from a repository")
	}
	out.Config.AIRationale = file.AIRationale
	if file.NeverAsk != nil {
		if *file.NeverAsk {
			refuse("never_ask", ProjectApprovalsFieldsForbidden, "a project can turn approvals back on but cannot turn them off")
		} else {
			out.Config.NeverAsk = file.NeverAsk
		}
	}
	return out
}

func trimApprovalRule(rule ApprovalRule) ApprovalRule {
	rule.Category = ApprovalCategory(strings.TrimSpace(string(rule.Category)))
	rule.Pattern = strings.TrimSpace(rule.Pattern)
	rule.Effect = ApprovalEffect(strings.TrimSpace(string(rule.Effect)))
	return rule
}
