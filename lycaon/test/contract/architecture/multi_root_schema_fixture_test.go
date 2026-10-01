package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func contractToolSchemas(t *testing.T) *toolschema.Config {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	return schemas
}

func contractArgsSchemaFor(reg *tools.DefaultRegistry, schemas *toolschema.Config, name string) map[string]any {
	if meta, ok := reg.Meta(name); ok && meta.ArgsSchema != nil {
		return meta.ArgsSchema
	}
	if meta, ok := schemas.ToolMeta(name); ok {
		return meta.ArgsSchema
	}
	return nil
}

func multiRootRequiredSet(schema map[string]any) map[string]bool {
	req := map[string]bool{}
	if schema == nil {
		return req
	}
	switch raw := schema["required"].(type) {
	case []any:
		for _, item := range raw {
			if s, ok := item.(string); ok {
				req[s] = true
			}
		}
	case []string:
		for _, s := range raw {
			req[s] = true
		}
	}
	return req
}

func multiRootPlantPathValue(propName, probePath string) any {
	if propName == "paths" {
		return []any{probePath}
	}
	return probePath
}

func multiRootPlantScalar(propName string, prop map[string]any) (any, bool) {
	if val, ok := multiRootModeArgProps[propName]; ok {
		return val, true
	}
	typ, _ := prop["type"].(string)
	switch typ {
	case "string":
		switch propName {
		case "pattern":
			return "x", true
		case "rewrite":
			return "y", true
		case "command":
			return "pwd", true
		case "mode":
			return "+x", true
		case "owner":
			return "current", true
		case "bundle":
			return "layout_overview", true
		case "content", "new_content":
			return "probe", true
		default:
			return "probe", true
		}
	case "integer":
		switch propName {
		case "start_line", "end_line":
			return 1, true
		default:
			return 1, true
		}
	case "boolean":
		return false, true
	default:
		return nil, false
	}
}

func multiRootPlantArgsFromSchema(schema map[string]any, probePath string, includePath bool) (map[string]any, bool) {
	if schema == nil {
		return nil, false
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return nil, false
	}
	required := multiRootRequiredSet(schema)
	args := map[string]any{}
	plantedPath := false

	for name, rawProp := range props {
		prop, _ := rawProp.(map[string]any)
		if prop == nil {
			continue
		}
		if _, isPath := multiRootPathArgProps[name]; isPath {
			if includePath {
				args[name] = multiRootPlantPathValue(name, probePath)
				plantedPath = true
			}
			continue
		}
		if required[name] {
			if v, ok := multiRootPlantScalar(name, prop); ok {
				args[name] = v
			}
		}
	}

	for name, rawProp := range props {
		prop, _ := rawProp.(map[string]any)
		if prop == nil {
			continue
		}
		typ, _ := prop["type"].(string)
		if typ != "array" {
			continue
		}
		items, _ := prop["items"].(map[string]any)
		if items == nil {
			continue
		}
		nested, _ := items["properties"].(map[string]any)
		if nested == nil {
			continue
		}
		entry := map[string]any{}
		nestedPlanted := false
		for sub := range nested {
			if _, isPath := multiRootPathArgProps[sub]; isPath {
				entry[sub] = probePath
				nestedPlanted = true
			} else if val, ok := multiRootModeArgProps[sub]; ok {
				entry[sub] = val
			} else if rawSub, ok := nested[sub]; ok {
				if subProp, ok := rawSub.(map[string]any); ok {
					if v, ok := multiRootPlantScalar(sub, subProp); ok {
						entry[sub] = v
					}
				}
			}
		}
		if nestedPlanted && (required[name] || includePath) {
			args[name] = []any{entry}
			plantedPath = true
		}
	}

	if !includePath {
		for name := range props {
			if _, isPath := multiRootPathArgProps[name]; !isPath {
				continue
			}
			args[name] = multiRootPlantPathValue(name, ".")
			plantedPath = true
		}
	}

	return args, plantedPath
}

func multiRootExerciseArgs(name, path string, includePath bool) map[string]any {
	if !includePath {
		path = "."
	}
	switch name {
	case "grep":
		return map[string]any{"pattern": "x", "path": path}
	case "find":
		return map[string]any{"path": path, "name_glob": "*.txt"}
	case "list_dir":
		return map[string]any{"path": path}
	case "command":
		return map[string]any{"command": "pwd"}
	case "stat", "wc", "delete", "chmod":
		return map[string]any{"paths": []any{path}}
	default:
		return map[string]any{"path": path}
	}
}
