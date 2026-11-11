package oar

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnreferencedDeclaredFactsFlagsOrphans(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "lycaon")
	anchors := filepath.Join(module, "config", "packs", "painted-wolf", "platform", "host", "anchors")
	if err := os.MkdirAll(anchors, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	profile := `oar_capability_version: "1.0"
host: paintedwolf
profiles: []
anchors:
  core:
    tool.pre_invoke: tool.pre_invoke
    tool.handler: tool.handler
    tool.post_invoke: tool.post_invoke
    agent.post_turn: coordinator.post_turn
    agent.finalize: worker.finalize
    model.input: content.input
  unsupported: [model.output, model.tool_result]
activity_window: 16
detectors: []
supports_transform: true
expression_nodes_max: 256
host_facts:
  - name: paintedwolf.audited_present
    type: bool
  - name: paintedwolf.audited_orphan
    type: bool
  - name: paintedwolf.can_ask_user
    type: bool
`
	if err := os.WriteFile(filepath.Join(anchors, "oar-profile.yaml"), []byte(profile), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	policy := filepath.Join(module, "config", "packs", "painted-wolf", "platform", "policy")
	if err := os.MkdirAll(policy, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	rule := "when: paintedwolf.audited_present == true\n"
	if err := os.WriteFile(filepath.Join(policy, "CONSUMER.yaml"), []byte(rule), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	got, err := UnreferencedDeclaredFacts(module)
	testutil.FailErr(t, "UnreferencedDeclaredFacts failed", err)
	// can_ask_user has no literal reference in the fixture tree but is in the
	// live recovery publication set, so only the truly unreferenced fact is
	// flagged.
	if len(got) != 1 || got[0] != "paintedwolf.audited_orphan" {
		t.Fatalf("unreferenced = %q, want [paintedwolf.audited_orphan]", got)
	}
}
