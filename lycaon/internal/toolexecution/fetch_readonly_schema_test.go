package toolexecution

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestFetchSchemaOnlyAdmitsResearchReads(t *testing.T) {
	catalog, err := toolschema.LoadSchemaDirWithOperations(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load tool schemas", err)
	meta, ok := catalog.ToolMeta("fetch_url")
	if !ok {
		t.Fatal("fetch schema missing")
	}
	for _, args := range []map[string]any{
		{"url": "https://example.test/page"},
		{"url": "https://example.test/page", "offset": 12, "limit": 25, "parallel": true},
	} {
		testutil.FailErr(t, "validate research read", tools.ValidateToolArgs(meta.ArgsSchema, args))
	}
	for _, args := range []map[string]any{
		{"url": "https://example.test/page", "dest": "output.txt"},
		{"url": "https://example.test/page", "mode": "raw"},
		{"url": "https://example.test/page", "mode": "text"},
		{"url": "https://example.test/page", "offset": 0},
	} {
		if tools.ValidateToolArgs(meta.ArgsSchema, args) == nil {
			t.Fatalf("invalid research read admitted: %v", args)
		}
	}
}
