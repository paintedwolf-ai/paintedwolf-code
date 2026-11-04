package guidance

import "testing"

func TestSpillRenderProbe(t *testing.T) {
	setTestRenderer(t)
	cfg, err := LoadHintConfigStock()
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]any{"spill_path": "tool-output/abc.txt", "spill_bytes": 120000, "original_bytes": 70000000, "max_file_bytes": 8388608, "spill_readable": true, "cap": 67108864}
	for _, code := range []string{"TOOL_OUTPUT_TRUNCATED", "TOOL_OUTPUT_SPILL_CAPPED"} {
		t.Logf("RENDER %s\n%s", code, EnvelopeHintMessage(t.Context(), cfg, code, vars))
	}
	t.Fatal("probe")
}
