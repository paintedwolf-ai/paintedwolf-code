package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestMergeModelCapabilityVars(t *testing.T) {
	vars := map[string]any{}
	prompts.MergeModelCapabilityVars(true, vars)
	caps, ok := vars["caps"].(map[string]any)
	if !ok || caps["vision"] != true {
		t.Fatalf("caps = %#v", vars["caps"])
	}
}
