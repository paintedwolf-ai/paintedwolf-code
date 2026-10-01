// Package parse implements the JSON parsing behind the parse_* tools.
package parse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonfence"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// DefaultService implements parse contracts for parse_* tools.
type DefaultService struct{}

// NewDefaultService constructs a filesystem-backed parse service.
func NewDefaultService() *DefaultService {
	return &DefaultService{}
}

// ExtractJSON pulls JSON from markdown fences or raw text.
func (s *DefaultService) ExtractJSON(_ context.Context, raw string, schema json.RawMessage) (*api.ExtractResult, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("raw text required")
	}
	payload := strings.TrimSpace(raw)
	source := "raw"
	if fenced, ok := jsonfence.First(raw); ok {
		payload = fenced
		source = "fence"
	}
	if !json.Valid([]byte(payload)) {
		return &api.ExtractResult{Extracted: false, SourceFence: source}, fmt.Errorf("invalid JSON payload")
	}
	out := &api.ExtractResult{
		JSON:        json.RawMessage(payload),
		Extracted:   true,
		SourceFence: source,
	}
	if len(schema) > 0 {
		result, err := validateAgainstSchema(payload, schema)
		if err != nil {
			return out, err
		}
		if result != nil && !result.Valid {
			return out, fmt.Errorf("schema validation failed: %v", result.Errors)
		}
	}
	return out, nil
}

// Validate checks raw JSON against an optional JSON Schema.
func (s *DefaultService) Validate(_ context.Context, raw string, schema json.RawMessage) (*api.ParseResult, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &api.ParseResult{Valid: false, Errors: []string{"empty payload"}}, nil
	}
	if !json.Valid([]byte(raw)) {
		return &api.ParseResult{Valid: false, Errors: []string{"invalid JSON"}}, nil
	}
	return validateAgainstSchema(raw, schema)
}

func validateAgainstSchema(raw string, schema json.RawMessage) (*api.ParseResult, error) {
	if len(schema) == 0 {
		return &api.ParseResult{Valid: true, Data: json.RawMessage(raw)}, nil
	}
	var schemaMap map[string]any
	if err := json.Unmarshal(schema, &schemaMap); err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return &api.ParseResult{Valid: false, Errors: []string{err.Error()}}, nil //nolint:nilerr // err.Error() carried in ParseResult.Errors
	}
	if err := tools.ValidateToolArgs(schemaMap, args); err != nil {
		return &api.ParseResult{Valid: false, Errors: []string{err.Error()}}, nil //nolint:nilerr // err.Error() carried in ParseResult.Errors
	}
	return &api.ParseResult{Valid: true, Data: json.RawMessage(raw)}, nil
}

// ValidateDecomposition validates leg decomposition JSON.
func (s *DefaultService) ValidateDecomposition(_ context.Context, raw string, constraints api.DecompositionConstraints) (*api.ParseResult, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &api.ParseResult{Valid: false, Errors: []string{"empty payload"}}, nil
	}
	var legs []api.DelegationLegPlan
	if err := json.Unmarshal([]byte(raw), &legs); err != nil {
		var wrapped struct {
			Legs []api.DelegationLegPlan `json:"legs"`
		}
		if err2 := json.Unmarshal([]byte(raw), &wrapped); err2 != nil {
			return &api.ParseResult{Valid: false, Errors: []string{"expected legs array or {legs:[]}"}}, nil //nolint:nilerr // tried both shapes; surface a stable user-facing message
		}
		legs = wrapped.Legs
	}
	var errs []string
	if constraints.MaxLegs > 0 && len(legs) > constraints.MaxLegs {
		errs = append(errs, fmt.Sprintf("too many legs: %d > %d", len(legs), constraints.MaxLegs))
	}
	for i, leg := range legs {
		if strings.TrimSpace(leg.Title) == "" {
			errs = append(errs, fmt.Sprintf("leg %d missing title", i))
		}
		if constraints.MaxFilesPerLeg > 0 && len(leg.Files) > constraints.MaxFilesPerLeg {
			errs = append(errs, fmt.Sprintf("leg %d too many files", i))
		}
		if constraints.RequireDeps {
			for _, dep := range leg.DependsOn {
				if strings.TrimSpace(dep) == "" {
					errs = append(errs, fmt.Sprintf("leg %d empty dependency", i))
				}
			}
		}
	}
	if len(errs) > 0 {
		return &api.ParseResult{Valid: false, Errors: errs}, nil
	}
	data, err := json.Marshal(legs)
	if err != nil {
		return nil, fmt.Errorf("marshal legs: %w", err)
	}
	return &api.ParseResult{Valid: true, Data: data}, nil
}

// ValidateDelegationPlan parses delegation plan JSON.
func (s *DefaultService) ValidateDelegationPlan(_ context.Context, raw string) (*api.DelegationPlan, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("raw plan required")
	}
	var plan api.DelegationPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, err
	}
	if strings.TrimSpace(plan.Task) == "" {
		return nil, fmt.Errorf("plan.task required")
	}
	if len(plan.Legs) == 0 {
		return nil, fmt.Errorf("plan.legs required")
	}
	return &plan, nil
}
