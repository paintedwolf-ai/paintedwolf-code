package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestManagedSecretRevocationSchemaAcceptsIssuedReference(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load lifecycle schemas", err)
	meta, ok := cfg.ToolMeta("secret_revoke")
	if !ok {
		t.Fatal("secret_revoke schema missing")
	}
	reference := secretmatch.ReferenceToken("123e4567-e89b-42d3-a456-426614174000")
	testutil.FailErr(t, "validate host-issued revocation reference", ValidateToolArgs(meta.ArgsSchema, map[string]any{"reference": reference}))
	for _, invalid := range []any{nil, 42, "", "Bearer " + reference, strings.TrimSuffix(reference, "}}"), "{{paintedwolf-secret:example}}"} {
		if err := ValidateToolArgs(meta.ArgsSchema, map[string]any{"reference": invalid}); err == nil {
			t.Fatalf("revocation schema accepted malformed reference %v", invalid)
		}
	}
}
