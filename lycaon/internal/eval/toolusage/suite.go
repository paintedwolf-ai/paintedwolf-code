package toolusage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/harnessfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// Suite separates useful outcomes from diagnostic tool preferences.
type Suite struct {
	TaskAllowance int               `yaml:"task_allowance,omitempty" json:"task_allowance,omitempty"`
	Unattended    *UnattendedPolicy `yaml:"unattended,omitempty" json:"unattended,omitempty"`
	ID            string            `yaml:"id" json:"id"`
	Cases         []SuiteCase       `yaml:"cases" json:"cases"`
	Digest        string            `yaml:"-" json:"sha256"`
	root          string
}

// ApprovalRule decides a candidate-raised approval card by its host-typed subject kind.
type ApprovalRule struct {
	Subject  string `yaml:"subject" json:"subject"`
	Decision string `yaml:"decision" json:"decision"`
	Rung     string `yaml:"rung,omitempty" json:"rung,omitempty"`
}

type SuiteCase struct {
	WorkflowVersion string                       `yaml:"workflow_version,omitempty" json:"workflow_version,omitempty"`
	WorkflowID      string                       `yaml:"workflow_id,omitempty" json:"workflow_id,omitempty"`
	Approvals       []ApprovalRule               `yaml:"approvals,omitempty" json:"approvals,omitempty"`
	SandboxStep     string                       `yaml:"sandbox_step,omitempty" json:"sandbox_step,omitempty"`
	PreludeFinal    string                       `yaml:"prelude_final,omitempty" json:"prelude_final,omitempty"`
	Prelude         []harnessfixture.PreludeStep `yaml:"prelude,omitempty" json:"prelude,omitempty"`
	Setup           *harnessfixture.Setup        `yaml:"setup,omitempty" json:"setup,omitempty"`
	CorpusTask      `yaml:",inline"`
	Sandbox         string   `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	Project         string   `yaml:"project" json:"project"`
	ReadOnly        bool     `yaml:"read_only,omitempty" json:"read_only"`
	Outcomes        []string `yaml:"outcomes" json:"outcomes"`
	Diagnostics     []string `yaml:"diagnostics,omitempty" json:"diagnostics"`
}

func LoadSuite(path string) (*Suite, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- explicit operator-selected suite.
	if err != nil {
		return nil, err
	}
	var suite Suite
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&suite); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("suite must contain one YAML document")
	}
	if strings.TrimSpace(suite.ID) == "" || len(suite.Cases) == 0 {
		return nil, fmt.Errorf("suite requires an id and at least one case")
	}
	if err := suite.Unattended.validate(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, c := range suite.Cases {
		if c.WorkflowID != "" && (!filepath.IsLocal(c.WorkflowID) || filepath.Base(c.WorkflowID) != c.WorkflowID) {
			return nil, fmt.Errorf("case %s requires a local workflow id", c.ID)
		}
		if (c.WorkflowID == "") != (c.WorkflowVersion == "") || (c.WorkflowID != "" && len(c.FollowUps) != 0) {
			return nil, fmt.Errorf("case %s requires a workflow version and one initial request", c.ID)
		}
		if len(c.Prelude) != 0 {
			if err := (harnessfixture.Prelude{OperationID: "00000000-0000-0000-0000-000000000001", Steps: c.Prelude}).Validate(); err != nil {
				return nil, fmt.Errorf("case %s: %w", c.ID, err)
			}
		}
		if c.Setup != nil {
			if err := c.Setup.Validate(); err != nil {
				return nil, fmt.Errorf("case %s: %w", c.ID, err)
			}
		}
		if c.PreludeFinal != "" && (len(c.Prelude) == 0 || len(c.FollowUps) == 0) {
			return nil, fmt.Errorf("case %q preparation handoff requires steps and a follow-up", c.ID)
		}
		if c.Sandbox != "" && c.Sandbox != sandboxWriteRoot {
			found := false
			for _, step := range c.Prelude {
				found = found || step.ID == c.SandboxStep
			}
			if !found {
				return nil, fmt.Errorf("case %q sandbox requires a declared preparation step", c.ID)
			}
		}
		if c.Sandbox != "" && (suite.Unattended == nil || !sandboxKinds[c.Sandbox]) {
			return nil, fmt.Errorf("case %q has an invalid unattended sandbox fixture", c.ID)
		}
		if err := validateApprovalRules(c.Approvals); err != nil {
			return nil, fmt.Errorf("case %q: %w", c.ID, err)
		}
		if strings.TrimSpace(c.ID) == "" || seen[c.ID] || strings.TrimSpace(c.Prompt) == "" || len(c.Outcomes) == 0 {
			return nil, fmt.Errorf("case %q needs a unique id, prompt, and outcome criteria", c.ID)
		}
		seen[c.ID] = true
		if !filepath.IsLocal(c.Project) {
			return nil, fmt.Errorf("case %q project must be relative to the suite directory", c.ID)
		}
		for _, f := range c.FollowUps {
			if strings.TrimSpace(f.Prompt) == "" {
				return nil, fmt.Errorf("case %q has an empty follow-up", c.ID)
			}
		}
	}
	sum := sha256.Sum256(body)
	suite.Digest = hex.EncodeToString(sum[:])
	suite.root, err = filepath.Abs(filepath.Dir(path))
	return &suite, err
}

func (s *Suite) Select(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	selected := make([]SuiteCase, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate case %q", id)
		}
		seen[id] = true
		found := false
		for _, c := range s.Cases {
			if c.ID == id {
				selected = append(selected, c)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown suite case %q", id)
		}
	}
	s.Cases = selected
	return nil
}

const (
	sandboxApproveRead  = "approve_read"
	sandboxDenyRead     = "deny_read"
	sandboxLoopback     = "loopback"
	sandboxDenyLoopback = "deny_loopback"
	sandboxWriteRoot    = "write_root"
)

var sandboxKinds = map[string]bool{sandboxApproveRead: true, sandboxDenyRead: true, sandboxLoopback: true, sandboxDenyLoopback: true, sandboxWriteRoot: true}

var approvalSubjectKinds = map[string]bool{
	string(wire.ApprovalSubjectKindAction): true, string(wire.ApprovalSubjectKindActionSet): true, string(wire.ApprovalSubjectKindSocketSet): true,
	string(wire.ApprovalSubjectKindDirectIP): true, string(wire.ApprovalSubjectKindLocalListen): true, string(wire.ApprovalSubjectKindLoopbackConnect): true,
	string(wire.ApprovalSubjectKindDestinationSet): true, string(wire.ApprovalSubjectKindWriteRootSet): true, string(wire.ApprovalSubjectKindReadPathSet): true,
	string(wire.ApprovalSubjectKindSecret): true, string(wire.ApprovalSubjectKindPackageSet): true,
}

func validateApprovalRules(rules []ApprovalRule) error {
	seen := map[string]bool{}
	for _, rule := range rules {
		if !approvalSubjectKinds[rule.Subject] || seen[rule.Subject] {
			return fmt.Errorf("approval rule requires a distinct host subject kind, got %q", rule.Subject)
		}
		seen[rule.Subject] = true
		switch rule.Decision {
		case "approve":
			if rule.Rung != approvalRungOnce && rule.Rung != approvalRungTask {
				return fmt.Errorf("approval rule for %q requires rung once or task", rule.Subject)
			}
		case "reject":
			if rule.Rung != "" {
				return fmt.Errorf("approval rule for %q rejects without a rung", rule.Subject)
			}
		default:
			return fmt.Errorf("approval rule for %q must approve or reject", rule.Subject)
		}
	}
	return nil
}
