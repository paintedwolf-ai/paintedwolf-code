package toolvocabtest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// DescriptionOrigin distinguishes host-authored native tool metadata from
// externally supplied or untrusted MCP metadata.
type DescriptionOrigin string

const (
	// DescriptionOriginHost marks native host-authored tool descriptions and schemas.
	DescriptionOriginHost DescriptionOrigin = "host"
	// DescriptionOriginExternal marks MCP or untrusted external tool metadata.
	DescriptionOriginExternal DescriptionOrigin = "external"
)

// ToolDescription is one model-facing description string projected on a ToolMeta
// or inside its recursive JSON Schema.
type ToolDescription struct {
	Tool      string
	FieldPath string
	Text      string
	Origin    DescriptionOrigin
}

// HostAuthored reports whether the description comes from host-authored native metadata.
func (d ToolDescription) HostAuthored() bool {
	return d.Origin == DescriptionOriginHost
}

func descriptionOriginForMeta(meta tools.ToolMeta) DescriptionOrigin {
	if meta.IsMCP() || meta.UntrustedMetadata {
		return DescriptionOriginExternal
	}
	return DescriptionOriginHost
}

// ExtractToolDescriptions collects top-level ToolMeta.Description and recursive
// JSON Schema property/subschema descriptions from projected ToolMeta entries.
func ExtractToolDescriptions(metas []tools.ToolMeta) []ToolDescription {
	var out []ToolDescription
	for _, meta := range metas {
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			continue
		}
		origin := descriptionOriginForMeta(meta)
		if desc := strings.TrimSpace(meta.Description); desc != "" {
			out = append(out, ToolDescription{
				Tool:   name,
				Text:   desc,
				Origin: origin,
			})
		}
		out = append(out, SchemaDescriptions(name, meta.ArgsSchema, origin)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		if out[i].FieldPath != out[j].FieldPath {
			return out[i].FieldPath < out[j].FieldPath
		}
		if out[i].Origin != out[j].Origin {
			return out[i].Origin < out[j].Origin
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// HostAuthoredToolDescriptions returns only host-authored native tool and schema
// descriptions from metas, excluding external MCP or untrusted metadata.
func HostAuthoredToolDescriptions(metas []tools.ToolMeta) []ToolDescription {
	all := ExtractToolDescriptions(metas)
	out := make([]ToolDescription, 0, len(all))
	for _, entry := range all {
		if entry.HostAuthored() {
			out = append(out, entry)
		}
	}
	return out
}

// SchemaDescriptions walks a JSON Schema map recursively and returns every
// non-empty string "description" with its dotted schema path.
func SchemaDescriptions(tool string, schema map[string]any, origin DescriptionOrigin) []ToolDescription {
	tool = strings.TrimSpace(tool)
	if tool == "" || len(schema) == 0 {
		return nil
	}
	var out []ToolDescription
	walkSchemaDescriptions(tool, "", schema, origin, &out)
	return out
}

func joinSchemaPath(prefix, segment string) string {
	if prefix == "" {
		return segment
	}
	if strings.HasPrefix(segment, "[") {
		return prefix + segment
	}
	return prefix + "." + segment
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func walkSchemaDescriptions(tool, path string, node map[string]any, origin DescriptionOrigin, out *[]ToolDescription) {
	if len(node) == 0 {
		return
	}
	// Record schema-node description at non-root paths (root ArgsSchema description,
	// when present without a property path, is recorded under "$schema").
	if raw, ok := node["description"].(string); ok {
		if text := strings.TrimSpace(raw); text != "" {
			fieldPath := path
			if fieldPath == "" {
				fieldPath = "$schema"
			}
			*out = append(*out, ToolDescription{
				Tool:      tool,
				FieldPath: fieldPath,
				Text:      text,
				Origin:    origin,
			})
		}
	}

	for _, mapKey := range []string{"properties", "patternProperties", "$defs", "definitions"} {
		subMap, ok := node[mapKey].(map[string]any)
		if !ok || len(subMap) == 0 {
			continue
		}
		for _, propName := range sortedMapKeys(subMap) {
			propNode, ok := subMap[propName].(map[string]any)
			if !ok {
				continue
			}
			subPath := joinSchemaPath(path, propName)
			if mapKey != "properties" {
				subPath = joinSchemaPath(path, mapKey+"."+propName)
			}
			walkSchemaDescriptions(tool, subPath, propNode, origin, out)
		}
	}

	switch items := node["items"].(type) {
	case map[string]any:
		walkSchemaDescriptions(tool, joinSchemaPath(path, "items"), items, origin, out)
	case []any:
		for idx, elem := range items {
			if elemMap, ok := elem.(map[string]any); ok {
				walkSchemaDescriptions(tool, joinSchemaPath(path, fmt.Sprintf("items[%d]", idx)), elemMap, origin, out)
			}
		}
	}

	for _, singleKey := range []string{"additionalProperties", "not", "if", "then", "else"} {
		if subNode, ok := node[singleKey].(map[string]any); ok {
			walkSchemaDescriptions(tool, joinSchemaPath(path, singleKey), subNode, origin, out)
		}
	}

	for _, listKey := range []string{"allOf", "anyOf", "oneOf"} {
		list, ok := node[listKey].([]any)
		if !ok {
			continue
		}
		for idx, elem := range list {
			if elemMap, ok := elem.(map[string]any); ok {
				walkSchemaDescriptions(tool, joinSchemaPath(path, fmt.Sprintf("%s[%d]", listKey, idx)), elemMap, origin, out)
			}
		}
	}
}
