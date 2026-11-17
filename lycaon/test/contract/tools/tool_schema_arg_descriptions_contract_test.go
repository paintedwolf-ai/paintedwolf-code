package contract

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestToolSchemaArgPropertiesHaveDescriptions walks every property in
// tools/schemas (including nested object/array item properties) and requires
// a non-empty description. Discovery is systems-driven from the loaded config —
// no allowlist of tools or args.
func TestToolSchemaArgPropertiesHaveDescriptions(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDirWithOperations(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	if cfg == nil || len(cfg.Tools) == 0 {
		t.Fatal("tools/schemas loaded empty tools map")
	}

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
		// Fragments under $defs compose model-facing members, so they carry
		// descriptions too.
		if defs, ok := entry.Schema["$defs"].(map[string]any); ok {
			collectUndescribedProperties(name, "$defs", map[string]any{"properties": defs}, &violations)
		}
	}
	contractcheck.FailViolations(t, "tools/schemas properties missing non-empty description", violations)
}

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
