package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStaticRejectFormatterFormatParse(t *testing.T) {
	SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	cfg := &HintConfig{
		HintCodes: map[string]HintEntry{
			"COORDINATOR_UNGROUNDED_CLAIM": {
				What: "claimed without a ledger job",
				Fix:  "Call delegate_dispatch for the leg",
			},
		},
	}
	f := NewStaticRejectFormatter(cfg)
	raw, err := f.Format("COORDINATOR_UNGROUNDED_CLAIM", nil)
	testutil.FailErr(t, "f.Format failed", err)
	if !strings.Contains(raw, "Code: COORDINATOR_UNGROUNDED_CLAIM") {
		t.Fatalf("missing code line: %q", raw)
	}
	block, err := f.Parse(raw)
	testutil.FailErr(t, "f.Parse failed", err)
	if block.Code != "COORDINATOR_UNGROUNDED_CLAIM" || block.What != "claimed without a ledger job" || block.Fix != "Call delegate_dispatch for the leg" {
		t.Fatalf("parsed = %+v", block)
	}
}

// [OAR-SEL-1] A presentation boundary must not broaden selector membership.
func TestRejectSelectorUsesExactMembership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected []string
		tool     string
		want     bool
	}{
		{"member", []string{"git_status", "read"}, "git_status", true},
		{"case differs", []string{"git_status"}, "GIT_STATUS", false},
		{"whitespace differs", []string{"read"}, " read ", false},
		{"pattern is literal", []string{"git_*"}, "git_status", false},
		{"literal member", []string{"git_*"}, "git_*", true},
		{"unrestricted", nil, "read", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := entryCoversTool("FIXTURE", HintEntry{Tools: tc.selected}, map[string]any{"tool": tc.tool})
			if (err == nil) != tc.want {
				t.Fatalf("selector %v, tool %q: error=%v, want match=%v", tc.selected, tc.tool, err, tc.want)
			}
		})
	}
}
