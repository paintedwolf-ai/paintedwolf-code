package guidance

import (
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"strings"
	"testing"
)

func TestDraftVersionOutcomeLabelFromCatalog(t *testing.T) {
	t.Helper()
	cfg, err := LoadHintConfig(extpacks.Bundled(hintregistry.DefaultDir))
	if err != nil {
		t.Fatalf("LoadHintConfig: %v", err)
	}
	if got := DraftVersionOutcomeLabel(cfg, ""); got != "Superseded" {
		t.Fatalf("blank code = %q want Superseded", got)
	}
	if got := DraftVersionOutcomeLabel(cfg, "NOT_A_REAL_CODE"); got != "Superseded" {
		t.Fatalf("unknown code = %q want Superseded", got)
	}
	got := DraftVersionOutcomeLabel(cfg, "SYNTH_HANDLE_NOT_IN_LEGS")
	if got == "" || got == "Superseded" {
		t.Fatalf("known code label = %q want hint message", got)
	}
	if !strings.Contains(got, "Synthesis") {
		t.Fatalf("known code label = %q want synthesis hint copy", got)
	}
}
