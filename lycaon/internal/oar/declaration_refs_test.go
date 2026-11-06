package oar

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnreferencedDeclaredFactsFlagsOrphans(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "lycaon")
	anchors := filepath.Join(module, "config", "packs", "painted-wolf", "platform", "host", "anchors")
	if err := os.MkdirAll(anchors, 0o755); err != nil {
		t.Fatal(err)
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
`
	if err := os.WriteFile(filepath.Join(anchors, "oar-profile.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(module, "config", "packs", "painted-wolf", "platform", "policy")
	if err := os.MkdirAll(policy, 0o755); err != nil {
		t.Fatal(err)
	}
	rule := "when: paintedwolf.audited_present == true\n"
	if err := os.WriteFile(filepath.Join(policy, "CONSUMER.yaml"), []byte(rule), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := UnreferencedDeclaredFacts(module)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "paintedwolf.audited_orphan" {
		t.Fatalf("unreferenced = %q, want [paintedwolf.audited_orphan]", got)
	}
}
