package presentation_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// Prompt tests cover each projected exit kind.

// The frame loader composes a review phase's call from the session's catalog;
// without a catalog source the phase offers the stock schema unchanged.

func shippedToolSchemas(t *testing.T) *toolschema.Config {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	return cfg
}

// catalogSubmitVerdictSchema loads the shipped submit_verdict call schema.
func catalogSubmitVerdictSchema(t *testing.T) map[string]any {
	t.Helper()
	meta, ok := shippedToolSchemas(t).ToolMeta("submit_verdict")
	if !ok {
		t.Fatal("submit_verdict schema missing")
	}
	return meta.ArgsSchema
}
