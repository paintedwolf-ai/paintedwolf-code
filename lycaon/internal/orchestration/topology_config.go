package orchestration

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// topologyFile is the YAML shape for config/packs/painted-wolf/platform/workflows/_topologies/*.yaml.
type topologyFile struct {
	ID            string          `yaml:"id"`
	Pattern       TopologyPattern `yaml:"pattern"`
	Task          string          `yaml:"task"`
	Criterion     string          `yaml:"criterion"`
	IterationCap  int             `yaml:"iteration_cap"`
	WorkspaceMode WorkspaceMode   `yaml:"workspace_mode"`
	BudgetUSD     *float64        `yaml:"budget_usd"`
	Supervisor    *supervisorYAML `yaml:"supervisor"`
	Pipeline      *pipelineYAML   `yaml:"pipeline"`
	FanOut        *fanOutYAML     `yaml:"fan_out"`
	Pack          *packYAML       `yaml:"pack"`
}

type supervisorYAML struct {
	Strategy   TeamStrategy `yaml:"strategy"`
	ProfileIDs []string     `yaml:"profile_ids"`
	MaxAgents  int          `yaml:"max_agents"`
}

type pipelineYAML struct {
	Stages []pipelineStageYAML `yaml:"stages"`
}

type pipelineStageYAML struct {
	Name         string   `yaml:"name"`
	Label        string   `yaml:"label"`
	Profile      string   `yaml:"profile"`
	InputFrom    []string `yaml:"input_from"`
	OutputFormat string   `yaml:"output_format"`
}

type fanOutYAML struct {
	Profile     string          `yaml:"profile"`
	Subtasks    []string        `yaml:"subtasks"`
	MaxWorkers  int             `yaml:"max_workers"`
	Aggregation AggregationMode `yaml:"aggregation"`
}

type packYAML struct {
	Count         int           `yaml:"count"`
	Profile       string        `yaml:"profile"`
	MergeStrategy MergeStrategy `yaml:"merge_strategy"`
}

// LoadTopologyFromFile parses a topology file.
func LoadTopologyFromFile(path extpacks.Source) (*TopologySpec, error) {
	data, err := path.Read()
	if err != nil {
		return nil, err
	}
	return ParseTopology(data)
}

// ParseTopology parses topology YAML bytes into TopologySpec.
func ParseTopology(data []byte) (*TopologySpec, error) {
	var raw topologyFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, err
	}
	if raw.ID == "" {
		return nil, fmt.Errorf("topology missing id")
	}
	if raw.Pattern == "" {
		return nil, fmt.Errorf("topology %q missing pattern", raw.ID)
	}
	spec := &TopologySpec{
		ID:            raw.ID,
		Pattern:       raw.Pattern,
		Task:          raw.Task,
		Criterion:     strings.TrimSpace(raw.Criterion),
		IterationCap:  raw.IterationCap,
		WorkspaceMode: raw.WorkspaceMode,
		BudgetUSD:     raw.BudgetUSD,
	}
	if err := validateWorkspaceMode(spec.WorkspaceMode); err != nil {
		return nil, fmt.Errorf("topology %q: %w", raw.ID, err)
	}
	if spec.IterationCap == 0 {
		spec.IterationCap = DefaultIterationCap
	}
	if raw.Supervisor != nil {
		spec.Supervisor = &SupervisorSpec{
			Strategy:   raw.Supervisor.Strategy,
			ProfileIDs: raw.Supervisor.ProfileIDs,
			MaxAgents:  raw.Supervisor.MaxAgents,
		}
	}
	if raw.Pipeline != nil {
		stages := make([]PipelineStage, len(raw.Pipeline.Stages))
		for i, s := range raw.Pipeline.Stages {
			label := strings.TrimSpace(s.Label)
			if label == "" {
				return nil, fmt.Errorf("topology %q pipeline stage %q: label required", raw.ID, s.Name)
			}
			stages[i] = PipelineStage{
				Name:         s.Name,
				Label:        label,
				AgentProfile: s.Profile,
				InputFrom:    s.InputFrom,
				OutputFormat: s.OutputFormat,
			}
		}
		if err := validatePipelineStages(stages); err != nil {
			return nil, fmt.Errorf("topology %q: %w", raw.ID, err)
		}
		spec.Pipeline = &PipelineSpec{Stages: stages}
	}
	if raw.FanOut != nil {
		fanOut := &FanOutSpec{
			ProfileID:   strings.TrimSpace(raw.FanOut.Profile),
			Subtasks:    raw.FanOut.Subtasks,
			MaxWorkers:  raw.FanOut.MaxWorkers,
			Aggregation: raw.FanOut.Aggregation,
		}
		if fanOut.ProfileID == "" {
			fanOut.ProfileID = ProfilePathExplorer
		}
		if fanOut.Aggregation == "" {
			fanOut.Aggregation = AggregationMerge
		}
		if err := validateFanOutSpec(*fanOut); err != nil {
			return nil, fmt.Errorf("topology %q fan_out: %w", raw.ID, err)
		}
		spec.FanOut = fanOut
	}
	if raw.Pack != nil {
		pack := &PackSpec{
			Count:         raw.Pack.Count,
			ProfileID:     strings.TrimSpace(raw.Pack.Profile),
			MergeStrategy: raw.Pack.MergeStrategy,
		}
		if pack.ProfileID == "" {
			pack.ProfileID = ProfilePathExplorer
		}
		if pack.MergeStrategy == "" {
			pack.MergeStrategy = MergeFirstValid
		}
		if err := validatePackSpec(*pack); err != nil {
			return nil, fmt.Errorf("topology %q pack: %w", raw.ID, err)
		}
		spec.Pack = pack
	}
	return spec, nil
}
