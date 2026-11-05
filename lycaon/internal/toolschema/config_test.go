package toolschema_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestParseEntryRejectsUnknownFields(t *testing.T) {
	if _, err := toolschema.ParseEntry([]byte("description: x\narg_aliases: {}\n")); err == nil {
		t.Fatal("unknown schema field accepted")
	}
}

func TestParseEntryAcceptsTags(t *testing.T) {
	entry, err := toolschema.ParseEntry([]byte("description: render\ntags: [visual, design]\nschema: {}\n"))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if len(entry.Tags) != 2 || entry.Tags[0] != "visual" || entry.Tags[1] != "design" {
		t.Fatalf("tags = %v want [visual design]", entry.Tags)
	}
}

func TestLoadBundledToolSchemasReadRequiresPath(t *testing.T) {
	dir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	cfg, err := toolschema.LoadSchemaDir(dir)
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	meta, ok := cfg.ToolMeta("read")
	if !ok {
		t.Fatal("read schema missing")
	}
	req, _ := toolschema.ArgFieldSummary(meta.ArgsSchema)
	if len(req) != 1 || req[0] != "path" {
		t.Fatalf("required = %v want [path]", req)
	}
}
