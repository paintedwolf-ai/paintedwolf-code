package parse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RegisterParseTools registers parse_* tools on the registry.
func RegisterParseTools(reg *tools.DefaultRegistry, svc *DefaultService) error {
	if reg == nil || svc == nil {
		return fmt.Errorf("registry and parse service required")
	}
	register := func(name string, handler tools.ToolHandler) error {
		return reg.Register(name, handler)
	}

	if err := register("parse_extract_json", func(ctx context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		raw, _ := args["raw"].(string)
		var schema json.RawMessage
		if s, ok := args["schema"].(string); ok && strings.TrimSpace(s) != "" {
			schema = json.RawMessage(s)
		}
		out, err := svc.ExtractJSON(ctx, raw, schema)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(data), nil
	}); err != nil {
		return err
	}

	if err := register("parse_validate", func(ctx context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		raw, _ := args["raw"].(string)
		var schema json.RawMessage
		if s, ok := args["schema"].(string); ok {
			schema = json.RawMessage(s)
		}
		out, err := svc.Validate(ctx, raw, schema)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(data), nil
	}); err != nil {
		return err
	}

	if err := register("parse_decomposition", func(ctx context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		raw, _ := args["raw"].(string)
		constraints := api.DecompositionConstraints{}
		if v, ok := args["max_legs"].(float64); ok {
			constraints.MaxLegs = int(v)
		}
		if v, ok := args["max_files_per_leg"].(float64); ok {
			constraints.MaxFilesPerLeg = int(v)
		}
		if v, ok := args["require_deps"].(bool); ok {
			constraints.RequireDeps = v
		}
		out, err := svc.ValidateDecomposition(ctx, raw, constraints)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(data), nil
	}); err != nil {
		return err
	}

	if err := register("parse_evaluation", func(ctx context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		raw, _ := args["raw"].(string)
		out, err := svc.Validate(ctx, raw, nil)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		return string(data), nil
	}); err != nil {
		return err
	}

	return register("parse_delegation_plan", func(ctx context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		raw, _ := args["raw"].(string)
		plan, err := svc.ValidateDelegationPlan(ctx, raw)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(plan)
		return string(data), nil
	})
}
