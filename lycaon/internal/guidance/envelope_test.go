package guidance

import "testing"

func TestEnvelopeHintMessageUsesRegistryMessage(t *testing.T) {
	cfg := &HintConfig{HintCodes: map[string]HintEntry{
		"SCAN_LIST_EMPTY": {
			Message: "No stored scans for this project.",
			What:    "No stored scans for this project.",
			Fix:     "Wait for scan_pack.",
		},
	}}
	got := EnvelopeHintMessage(t.Context(), cfg, "SCAN_LIST_EMPTY", nil)
	if got != "No stored scans for this project.\nWait for scan_pack." {
		t.Fatalf("got %q", got)
	}
}
