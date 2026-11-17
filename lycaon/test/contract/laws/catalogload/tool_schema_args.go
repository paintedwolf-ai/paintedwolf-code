package catalogload

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/test/contract/laws"
)

var toolSchemaArgs = []laws.Rule{{
	// Every tool and every schema property, nested and $defs fragments
	// included, carries a description the model can read.
	Name:    "tool-schema-properties-are-described",
	Anchors: []laws.Anchor{laws.Func("internal/toolschema", "LoadSchemaDirWithOperations")},
	Check: func(r *laws.Run) {
		cfg, err := toolschema.LoadSchemaDirWithOperations(r.Path(schemasDir))
		r.Must(err)
		r.Expect(len(cfg.Tools), "tool schemas")
		names := make([]string, 0, len(cfg.Tools))
		for name := range cfg.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		var violations []string
		for _, name := range names {
			entry := cfg.Tools[name]
			if strings.TrimSpace(entry.Description) == "" {
				violations = append(violations, fmt.Sprintf("%s: tool description is empty", name))
			}
			collectUndescribedProperties(name, "", entry.Schema, &violations)
			if defs, ok := entry.Schema["$defs"].(map[string]any); ok {
				collectUndescribedProperties(name, "$defs", map[string]any{"properties": defs}, &violations)
			}
		}
		for _, v := range violations {
			r.Report("%s", v)
		}
	},
}}

func collectUndescribedProperties(tool, prefix string, node map[string]any, out *[]string) {
	if node == nil {
		return
	}
	props, _ := node["properties"].(map[string]any)
	if props == nil {
		return
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		raw := props[key]
		child, ok := raw.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s.%s: property schema must be a map with description", tool, path))
			continue
		}
		desc, _ := child["description"].(string)
		if strings.TrimSpace(desc) == "" {
			*out = append(*out, fmt.Sprintf("%s.%s", tool, path))
		}
		collectUndescribedProperties(tool, path, child, out)
		if items, ok := child["items"].(map[string]any); ok {
			collectUndescribedProperties(tool, path+"[]", items, out)
		}
	}
}
