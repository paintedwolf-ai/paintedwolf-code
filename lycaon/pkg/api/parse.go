package api

import "encoding/json"

// ParseResult is the outcome of parse validation.
type ParseResult struct {
	Valid  bool            `json:"valid"`
	Errors []string        `json:"errors,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// DecompositionConstraints bounds delegation decomposition validation.
type DecompositionConstraints struct {
	MaxLegs        int  `json:"max_legs"`
	MaxFilesPerLeg int  `json:"max_files_per_leg"`
	RequireDeps    bool `json:"require_deps"`
}

// ExtractResult is JSON extracted from markdown fences.
type ExtractResult struct {
	JSON        json.RawMessage `json:"json,omitempty"`
	Extracted   bool            `json:"extracted"`
	SourceFence string          `json:"source_fence,omitempty"`
}

// DelegationLegPlan is one leg in a parsed delegation plan.
type DelegationLegPlan struct {
	Title     string   `json:"title"`
	Files     []string `json:"files"`
	DependsOn []string `json:"depends_on,omitempty"`
	Prompt    string   `json:"prompt,omitempty"`
}

// DelegationPlan is structured delegation plan JSON from parse_delegation_plan.
type DelegationPlan struct {
	Task     string              `json:"task"`
	Strategy HuntStrategy        `json:"strategy,omitempty"`
	Legs     []DelegationLegPlan `json:"legs"`
}
