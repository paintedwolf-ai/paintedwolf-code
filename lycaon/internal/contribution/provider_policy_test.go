package contribution

import "testing"

// Every action kind has one provider policy.
func TestProviderPolicyTableIsClosed(t *testing.T) {
	want := map[ActionKind]ProviderPolicy{
		ActionEditorAction:    PolicyTrustedPack,
		ActionWorkflowStart:   PolicyTrustedPack,
		ActionMCPTool:         PolicyTrustedPack,
		ActionComposerPrefill: PolicyTrustedPack,
		ActionNavigate:        PolicyTrustedPack,
		ActionExternalLink:    PolicyTrustedPack,
		ActionNativeUI:        PolicyStockOnly,
		ActionOperation:       PolicyTrustedPack,
	}
	if len(want) != len(actionKinds) {
		t.Fatalf("policy table covers %d kinds, union has %d", len(want), len(actionKinds))
	}
	for kind := range actionKinds {
		expected, ok := want[kind]
		if !ok {
			t.Fatalf("action kind %s has no expected policy row", kind)
		}
		if got := ProviderPolicyFor(kind); got != expected {
			t.Fatalf("ProviderPolicyFor(%s) = %s want %s", kind, got, expected)
		}
	}
}
