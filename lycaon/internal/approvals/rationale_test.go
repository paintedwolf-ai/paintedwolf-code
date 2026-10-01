package approvals_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatRationaleUserPrompt_ordersArcFirst(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	prompt, err := approvals.FormatRationaleUserPrompt(context.Background(), approvals.RationaleInputs{
		UserIntent:      "Ship the auth fix",
		WorkerBrief:     "Land overlay then push",
		ProgressSteps:   []api.ProgressStep{{State: "pending", Label: "Push"}},
		Tool:            "command",
		Args:            map[string]any{"command": "git push"},
		AssistantProse:  "Pushing now.",
		ExplanationWhat: "Push branch to remote.",
	})
	testutil.FailErr(t, "FormatRationaleUserPrompt", err)
	userIdx := strings.Index(prompt, "User goal:")
	workerIdx := strings.Index(prompt, "Worker assignment:")
	planIdx := strings.Index(prompt, "- [pending] Push")
	actionIdx := strings.Index(prompt, "Gated action:")
	assistIdx := strings.Index(prompt, "Assistant note")
	if userIdx < 0 || workerIdx < 0 || planIdx < 0 || actionIdx < 0 || assistIdx < 0 {
		t.Fatalf("missing sections:\n%s", prompt)
	}
	if userIdx >= workerIdx || workerIdx >= planIdx || planIdx >= actionIdx || actionIdx >= assistIdx {
		t.Fatalf("section order wrong:\n%s", prompt)
	}
	if strings.Contains(prompt, "Ship the auth fix") == false {
		t.Fatal("expected user intent in prompt")
	}
}

func TestClipRationale_keepsOneSentenceVerbatim(t *testing.T) {
	got := approvals.ClipRationale(`  "Sets up the watcher the user asked for."  `)
	if got != "Sets up the watcher the user asked for." {
		t.Fatalf("got %q", got)
	}
}

func TestClipRationale_dropsSecondSentence(t *testing.T) {
	got := approvals.ClipRationale("Sets up the watcher. This advances the goal by rebuilding on change.")
	if got != "Sets up the watcher." {
		t.Fatalf("got %q", got)
	}
}

func TestClipRationale_keepsInnerDotsIntact(t *testing.T) {
	const s = "Rebuilds main.go on change so the v1.2 server stays live."
	if got := approvals.ClipRationale(s); got != s {
		t.Fatalf("got %q", got)
	}
}

func TestClipRationale_clampsRunawaySentenceAtWordBoundary(t *testing.T) {
	got := approvals.ClipRationale(strings.Repeat("word ", 80) + "end.")
	if n := len([]rune(got)); n > approvals.MaxRationaleRunes+1 {
		t.Fatalf("len = %d", n)
	}
	if !strings.HasSuffix(got, "word…") {
		t.Fatalf("expected a word-boundary clamp, got %q", got)
	}
}

func TestClipRationale_empty(t *testing.T) {
	if got := approvals.ClipRationale("   "); got != "" {
		t.Fatalf("got %q", got)
	}
}
