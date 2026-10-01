package harnessfixture

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// StageKind names what a scripted worker does on one execution.
type StageKind string

const (
	StageComplete      StageKind = "complete"
	StageNeedsDecision StageKind = "needs_decision"
	StageFailed        StageKind = "failed"
)

const (
	maxScriptStages    = 4
	maxDecisionOptions = 4
	maxFindings        = 8
)

// Delivery is the change a write leg lands on its branch, verified before return.
type Delivery struct {
	Files  map[string]string `json:"files" yaml:"files"`
	Verify string            `json:"verify" yaml:"verify"`
}

// Finding is an observation a read leg reports; the excerpt comes from the file.
type Finding struct {
	Path string `json:"path" yaml:"path"`
	Line int    `json:"line" yaml:"line"`
	Note string `json:"note" yaml:"note"`
}

// WorkerStage is one scripted execution of a worker child.
type WorkerStage struct {
	Kind     StageKind           `json:"kind" yaml:"kind"`
	Files    map[string]string   `json:"files,omitempty" yaml:"files,omitempty"`
	Verify   string              `json:"verify,omitempty" yaml:"verify,omitempty"`
	Findings []Finding           `json:"findings,omitempty" yaml:"findings,omitempty"`
	Question string              `json:"question,omitempty" yaml:"question,omitempty"`
	Options  []string            `json:"options,omitempty" yaml:"options,omitempty"`
	Outcomes map[string]Delivery `json:"outcomes,omitempty" yaml:"outcomes,omitempty"`
	Code     string              `json:"code,omitempty" yaml:"code,omitempty"`
}

// WorkerScript fixes what a prepared child returns on each later execution.
type WorkerScript struct {
	Stages []WorkerStage `json:"stages" yaml:"stages"`
}

// DispatchScript fixes what a fresh worker returns for one declared scope.
type DispatchScript struct {
	Label  string        `json:"label" yaml:"label"`
	Mode   string        `json:"mode" yaml:"mode"`
	Paths  []string      `json:"paths" yaml:"paths"`
	Stages []WorkerStage `json:"stages" yaml:"stages"`
}

func (s WorkerScript) Validate() error {
	return validateStages(s.Stages, api.TaskScopeModeWrite)
}

func (d DispatchScript) Validate() error {
	if strings.TrimSpace(d.Label) == "" {
		return fmt.Errorf("dispatch script requires a label")
	}
	mode := api.TaskScopeMode(d.Mode)
	if mode != api.TaskScopeModeRead && mode != api.TaskScopeModeWrite {
		return fmt.Errorf("dispatch script %q mode must be read or write", d.Label)
	}
	if len(d.Paths) == 0 || len(d.Paths) > 12 {
		return fmt.Errorf("dispatch script %q requires one to twelve paths", d.Label)
	}
	seen := map[string]bool{}
	for _, path := range d.Paths {
		if err := validateFiles(map[string]string{path: ""}); err != nil {
			return err
		}
		if seen[path] {
			return fmt.Errorf("dispatch script %q repeats path %q", d.Label, path)
		}
		seen[path] = true
	}
	return validateStages(d.Stages, mode)
}

// Key identifies the dispatch scope bound to this script.
func (d DispatchScript) Key() string {
	return dispatchKey(d.Mode, d.Paths)
}

func dispatchKey(mode string, paths []string) string {
	unique := map[string]bool{}
	for _, value := range paths {
		unique[path.Clean(value)] = true
	}
	sorted := make([]string, 0, len(unique))
	for value := range unique {
		sorted = append(sorted, value)
	}
	sort.Strings(sorted)
	return mode + ":" + strings.Join(sorted, "\x00")
}

func validateStages(stages []WorkerStage, mode api.TaskScopeMode) error {
	if len(stages) == 0 || len(stages) > maxScriptStages {
		return fmt.Errorf("worker script requires one to %d stages", maxScriptStages)
	}
	for i, stage := range stages {
		last := i == len(stages)-1
		switch stage.Kind {
		case StageComplete:
			if err := validateDeliveryFor(mode, Delivery{Files: stage.Files, Verify: stage.Verify}, stage.Findings); err != nil {
				return err
			}
			if stage.Question != "" || len(stage.Options) != 0 || len(stage.Outcomes) != 0 || stage.Code != "" {
				return fmt.Errorf("complete stage carries only its delivery")
			}
		case StageNeedsDecision:
			if mode != api.TaskScopeModeWrite {
				return fmt.Errorf("decision stages require a write leg")
			}
			if strings.TrimSpace(stage.Question) == "" || len(stage.Options) < 2 || len(stage.Options) > maxDecisionOptions {
				return fmt.Errorf("decision stage requires a question and two to %d options", maxDecisionOptions)
			}
			seen := map[string]bool{}
			for _, option := range stage.Options {
				if strings.TrimSpace(option) == "" || seen[option] {
					return fmt.Errorf("decision options must be distinct and nonempty")
				}
				seen[option] = true
				delivery, ok := stage.Outcomes[option]
				if !ok {
					return fmt.Errorf("decision option %q has no outcome", option)
				}
				if err := validateDeliveryFor(mode, delivery, nil); err != nil {
					return err
				}
			}
			if len(stage.Outcomes) != len(stage.Options) || len(stage.Files) != 0 || stage.Verify != "" || len(stage.Findings) != 0 || stage.Code != "" {
				return fmt.Errorf("decision stage carries only its question, options, and outcomes")
			}
		case StageFailed:
			if !last {
				return fmt.Errorf("a failed stage ends the script")
			}
			if strings.TrimSpace(stage.Code) == "" || len(stage.Files) != 0 || stage.Verify != "" || len(stage.Findings) != 0 || stage.Question != "" {
				return fmt.Errorf("failed stage carries only its code")
			}
		default:
			return fmt.Errorf("unknown worker stage kind %q", stage.Kind)
		}
	}
	return nil
}

func validateDeliveryFor(mode api.TaskScopeMode, delivery Delivery, findings []Finding) error {
	if mode == api.TaskScopeModeWrite {
		if len(delivery.Files) == 0 || strings.TrimSpace(delivery.Verify) == "" {
			return fmt.Errorf("write delivery requires files and verification")
		}
		if len(findings) != 0 {
			return fmt.Errorf("write delivery does not report findings")
		}
		return validateFiles(delivery.Files)
	}
	if len(delivery.Files) != 0 || delivery.Verify != "" {
		return fmt.Errorf("read delivery carries findings only")
	}
	if len(findings) == 0 || len(findings) > maxFindings {
		return fmt.Errorf("read delivery requires one to %d findings", maxFindings)
	}
	for _, finding := range findings {
		if err := validateFiles(map[string]string{finding.Path: ""}); err != nil {
			return err
		}
		if finding.Line < 1 || strings.TrimSpace(finding.Note) == "" {
			return fmt.Errorf("finding requires a positive line and a note")
		}
	}
	return nil
}
