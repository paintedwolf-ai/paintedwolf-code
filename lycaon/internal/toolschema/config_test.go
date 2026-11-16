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

func TestParseEntryRejectsUnknownDescriptionEffectKey(t *testing.T) {
	raw := []byte("description: x\ndescription_effects:\n  not_an_effect: extra\nschema: {}\n")
	if _, err := toolschema.ParseEntry(raw); err == nil {
		t.Fatal("unknown description_effects key accepted")
	}
}

func TestParseEntryAcceptsValidDescriptionEffects(t *testing.T) {
	raw := []byte("description: base\ndescription_effects:\n  mutation: m\n  external_exec: e\n  external_exec_selector: s\n  network_egress: n\nschema: {}\n")
	entry, err := toolschema.ParseEntry(raw)
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	if len(entry.DescriptionEffects) != 4 {
		t.Fatalf("description_effects = %v, want 4 keys", entry.DescriptionEffects)
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
	dir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "operation-schemas")
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
