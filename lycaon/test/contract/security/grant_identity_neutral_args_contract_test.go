package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// An argument named in grant_identity_neutral_args but absent from the tool's
// schema silently does nothing: the grant keeps hashing the real argument and the
// human keeps re-answering the same action. Bind the catalog to the schema.
func TestGrantIdentityNeutralArgsExistInToolSchemas(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	if len(cfg.GrantIdentityNeutralArgs) == 0 {
		t.Fatal("grant_identity_neutral_args is empty — the grant identity hashes every argument again")
	}
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(
		contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load tool schemas", err)

	for _, row := range cfg.GrantIdentityNeutralArgRows() {
		meta, ok := schemas.ToolMeta(row.Tool)
		if !ok {
			t.Errorf("grant_identity_neutral_args names %q, which has no tool schema", row.Tool)
			continue
		}
		properties, _ := meta.ArgsSchema["properties"].(map[string]any)
		for _, arg := range row.Args {
			if _, present := properties[arg]; !present {
				t.Errorf("%s schema has no %q property — the neutral-argument entry is dead", row.Tool, arg)
			}
		}
	}
}

// The compiled table is what GrantKey reads; a codegen skew would ship a policy
// nobody wrote.
func TestGrantIdentityNeutralArgsCompileFromTheCatalog(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	for _, row := range cfg.GrantIdentityNeutralArgRows() {
		compiled := toolcontract.GrantIdentityNeutralArgs(row.Tool)
		if len(compiled) != len(row.Args) {
			t.Errorf("%s: catalog has %d neutral arguments, compiled table has %d — run ./task codegen:native-tool-contracts",
				row.Tool, len(row.Args), len(compiled))
			continue
		}
		for i, arg := range row.Args {
			if compiled[i] != arg {
				t.Errorf("%s: catalog %q vs compiled %q — run ./task codegen:native-tool-contracts", row.Tool, arg, compiled[i])
			}
		}
	}
}
