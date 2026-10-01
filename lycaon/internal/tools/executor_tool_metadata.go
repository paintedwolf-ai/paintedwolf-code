package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// SetToolSchemas wires native tool schema SSOT for normalization and reject detail.
func (e *DefaultToolExecutor) SetToolSchemas(cfg *toolschema.Config) {
	if e != nil {
		e.toolSchemas = cfg
	}
}

// SetToolSchemaSource installs a per-session schema resolver. Nil clears it.
func (e *DefaultToolExecutor) SetToolSchemaSource(fn func(ctx context.Context, sessionID string) *toolschema.Config) {
	if e != nil {
		e.schemaSource = fn
	}
}

func (e *DefaultToolExecutor) schemasFor(ctx context.Context, sessionID string) *toolschema.Config {
	if e == nil {
		return nil
	}
	if e.schemaSource != nil {
		if cfg := e.schemaSource(ctx, sessionID); cfg != nil {
			return cfg
		}
	}
	return e.toolSchemas
}

func (e *DefaultToolExecutor) argsSchemaFor(ctx context.Context, sessionID, tool string) map[string]any {
	if cfg := e.schemasFor(ctx, sessionID); cfg != nil {
		if meta, ok := cfg.ToolMeta(tool); ok {
			return meta.ArgsSchema
		}
	}
	if e != nil && e.registry != nil {
		if meta, ok := e.registry.Meta(tool); ok {
			return meta.ArgsSchema
		}
	}
	return nil
}

// List returns tools from the underlying registry filtered by profile when set.
func (e *DefaultToolExecutor) List(ctx context.Context, filter platform.ToolFilter) ([]ToolMeta, error) {
	all := e.registry.List()
	if filter.ProfileID == "" {
		return all, nil
	}
	out := make([]ToolMeta, 0, len(all))
	for _, meta := range all {
		decision, err := EvaluateListVisible(ctx, e.policy, platform.PolicyContext{
			ProfileID:  filter.ProfileID,
			ToolAccess: filter.ToolAccess,
			ToolName:   meta.Name,
		})
		if err != nil {
			continue
		}
		if decision != nil && decision.Allowed {
			enriched := e.enrichToolMeta(meta, filter)
			enriched.Deferred = decision.Deferred
			out = append(out, enriched)
		}
	}
	return out, nil
}

func (e *DefaultToolExecutor) enrichToolMeta(meta ToolMeta, filter platform.ToolFilter) ToolMeta {
	profileID := filter.ProfileID
	if profileID == "" {
		return meta
	}
	switch meta.Name {
	case "wait":
		if source, ok := e.policy.(interface{ WaitConditions(string) []string }); ok {
			meta.ArgsSchema = pruneWaitConditionSchema(meta.ArgsSchema, source.WaitConditions(profileID))
		}
	case "command":
		if inverse := CommandInverseSurveyLine(profileID); inverse != "" {
			meta.Description = strings.TrimSpace(meta.Description) + " " + inverse
		}
	default:
		if suffix := NativeReplacesCommandSuffix(meta.Name, profileID); suffix != "" {
			meta.Description = appendReplacesSuffix(meta.Description, suffix)
		}
	}
	return meta
}

func pruneWaitConditionSchema(schema map[string]any, allowed []string) map[string]any {
	cloned := jsonvalue.CloneMap(schema)
	properties, _ := cloned["properties"].(map[string]any)
	if len(allowed) == 0 {
		delete(properties, "conditions")
		return cloned
	}
	conditions, _ := properties["conditions"].(map[string]any)
	items, _ := conditions["items"].(map[string]any)
	itemProperties, _ := items["properties"].(map[string]any)
	kind, _ := itemProperties["kind"].(map[string]any)
	values := make([]any, 0, len(allowed))
	for _, value := range allowed {
		values = append(values, value)
	}
	kind["enum"] = values
	return cloned
}
