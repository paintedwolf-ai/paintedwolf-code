package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestVerifyStoreProjectOverlay(t *testing.T) {
	dir := t.TempDir()
	s := &VerifyStore{projectCache: map[string]VerifyConfig{}}

	if got := s.VerifyTestCommand(dir); got != "" {
		t.Fatalf("undeclared = %q want empty", got)
	}

	if err := s.PutProject(dir, VerifyConfig{Test: "  ./task check  "}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	if got := s.VerifyTestCommand(dir); got != "./task check" {
		t.Fatalf("declared = %q want normalized ./task check", got)
	}
	// A fresh store reloads the persisted overlay.
	if _, err := os.Stat(filepath.Join(dir, settingsoverlay.DirName(), "verify.yaml")); err != nil {
		t.Fatalf("verify.yaml not written: %v", err)
	}
	fresh := &VerifyStore{projectCache: map[string]VerifyConfig{}}
	if got := fresh.Get(llm.SettingsScopeProject, dir).Test; got != "./task check" {
		t.Fatalf("reload = %q want ./task check", got)
	}
}

func seedDoc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func TestDetectVerifyCommandLLM(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("guidance/utility/verify-detect-system.md", "Extract verify command"); err != nil {
		t.Fatalf("engine.Register: %v", err)
	}
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	dir := t.TempDir()
	seedDoc(t, dir, "README.md", "## Testing\nRun `./task check` before committing.\n")

	t.Run("proposes documented command", func(t *testing.T) {
		sum := compaction.MockSummarizer{Text: `{"command":"./task check","source":"README.md"}`}
		cand, outcome := DetectVerifyCommandLLM(context.Background(), sum, []string{dir})
		if outcome != VerifyDetectFound || cand.Command != "./task check" || cand.Source != "README.md" {
			t.Fatalf("DetectVerifyCommandLLM = (%+v,%v) want ./task check from README.md", cand, outcome)
		}
	})

	// A valid no-command answer is cacheable.
	t.Run("model finds no command", func(t *testing.T) {
		sum := compaction.MockSummarizer{Text: `{"command":""}`}
		cand, outcome := DetectVerifyCommandLLM(context.Background(), sum, []string{dir})
		if outcome != VerifyDetectNoCommand || cand.Command != "" {
			t.Fatalf("empty command = (%+v,%v) want a cacheable no-command answer", cand, outcome)
		}
	})

	t.Run("nil summarizer", func(t *testing.T) {
		if _, outcome := DetectVerifyCommandLLM(context.Background(), nil, []string{dir}); outcome.Ran() {
			t.Fatalf("nil summarizer must report unavailable, not an answer")
		}
	})

	// Summarizer failures stay uncached.
	t.Run("summarizer error", func(t *testing.T) {
		sum := compaction.MockSummarizer{Err: errors.New("no provider")}
		if _, outcome := DetectVerifyCommandLLM(context.Background(), sum, []string{dir}); outcome.Ran() {
			t.Fatalf("errored summarizer must report unavailable, not an answer")
		}
	})

	t.Run("no docs short-circuits", func(t *testing.T) {
		// Empty documentation skips the summarizer.
		sum := compaction.MockSummarizer{Text: `{"command":"./task check"}`}
		cand, outcome := DetectVerifyCommandLLM(context.Background(), sum, []string{t.TempDir()})
		// Empty documentation is a cacheable project result.
		if outcome != VerifyDetectNoCommand || cand.Command != "" {
			t.Fatalf("no docs = (%+v,%v) want a cacheable no-command answer", cand, outcome)
		}
	})

	t.Run("parses reply wrapped in prose", func(t *testing.T) {
		sum := compaction.MockSummarizer{Text: "Sure! Here you go:\n```json\n{\"command\":\"make test\",\"source\":\"AGENTS.md\"}\n```"}
		cand, outcome := DetectVerifyCommandLLM(context.Background(), sum, []string{dir})
		if outcome != VerifyDetectFound || cand.Command != "make test" {
			t.Fatalf("wrapped reply = (%+v,%v) want make test", cand, outcome)
		}
	})
}

func TestReadVerifyDetectDocsDedup(t *testing.T) {
	dir := t.TempDir()
	// Identical documents appear once.
	seedDoc(t, dir, "AGENTS.md", "shared policy body")
	seedDoc(t, dir, "README.md", "shared policy body")
	docs := readVerifyDetectDocs([]string{dir})
	if n := strings.Count(docs, "shared policy body"); n != 1 {
		t.Fatalf("duplicate doc content emitted %d times, want 1", n)
	}
}

func TestVerifyStoreProposalCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := NewVerifyStore()
	if err != nil {
		t.Fatalf("NewVerifyStore: %v", err)
	}
	const dir = "/tmp/project-a"

	if _, ok := s.ProposalFor(dir); ok {
		t.Fatalf("fresh store must have no proposal")
	}
	s.SetProposal(dir, VerifyDetectCandidate{Command: "./task check", Source: "README.md"})
	cand, ok := s.ProposalFor(dir)
	if !ok || cand.Command != "./task check" {
		t.Fatalf("ProposalFor = (%+v,%v) want ./task check", cand, ok)
	}

	// Reloads from disk on a fresh store (survives sidecar restart).
	fresh, err := NewVerifyStore()
	if err != nil {
		t.Fatalf("reload NewVerifyStore: %v", err)
	}
	if cand, ok := fresh.ProposalFor(dir); !ok || cand.Command != "./task check" {
		t.Fatalf("reloaded proposal = (%+v,%v) want ./task check", cand, ok)
	}

	// Empty candidate is a valid "detected nothing" marker that suppresses re-detection.
	s.SetProposal(dir, VerifyDetectCandidate{})
	if cand, ok := s.ProposalFor(dir); !ok || cand.Command != "" {
		t.Fatalf("empty marker = (%+v,%v) want present+empty", cand, ok)
	}

	s.ClearProposal(dir)
	if _, ok := s.ProposalFor(dir); ok {
		t.Fatalf("ClearProposal left a proposal")
	}
}

func TestVerifyStoreDismissProposal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := NewVerifyStore()
	if err != nil {
		t.Fatalf("NewVerifyStore: %v", err)
	}
	const dir = "/tmp/project-dismiss"
	s.SetProposal(dir, VerifyDetectCandidate{Command: "./task check", Source: "README.md"})

	s.DismissProposal(dir, "2026-07-06T00:00:00Z")
	cand, ok := s.ProposalFor(dir)
	if !ok || !cand.Dismissed || cand.DismissedAt == "" {
		t.Fatalf("after dismiss = (%+v,%v) want dismissed with timestamp", cand, ok)
	}
	// Dismissal silences the nudge but preserves detection.
	if cand.Command != "./task check" {
		t.Fatalf("dismiss must not clear the detected command: %q", cand.Command)
	}

	// Sticky across a sidecar restart.
	fresh, err := NewVerifyStore()
	if err != nil {
		t.Fatalf("reload NewVerifyStore: %v", err)
	}
	if cand, ok := fresh.ProposalFor(dir); !ok || !cand.Dismissed {
		t.Fatalf("reloaded dismiss = (%+v,%v) want dismissed", cand, ok)
	}

	s.UndismissProposal(dir)
	if cand, ok := s.ProposalFor(dir); !ok || cand.Dismissed || cand.DismissedAt != "" {
		t.Fatalf("after undismiss = (%+v,%v) want not-dismissed", cand, ok)
	}
}

func TestVerifyToDTOSuggestionState(t *testing.T) {
	const dir = "/tmp/project-dto"
	proj := wire.SettingsScopeProject
	cases := []struct {
		name    string
		cfg     VerifyConfig
		detect  VerifyDetectCandidate
		hasDet  bool
		want    wire.VerifySuggestionState
		wantCmd string
	}{
		{"unknown-no-detect", VerifyConfig{}, VerifyDetectCandidate{}, false, wire.VerifySuggestionStateUnknown, ""},
		{"waiting-empty-detect", VerifyConfig{}, VerifyDetectCandidate{}, true, wire.VerifySuggestionStateWaiting, ""},
		{"suggest", VerifyConfig{}, VerifyDetectCandidate{Command: "./task check", Source: "README.md"}, true, wire.VerifySuggestionStateSuggest, "./task check"},
		{"dismissed", VerifyConfig{}, VerifyDetectCandidate{Command: "./task check", Dismissed: true}, true, wire.VerifySuggestionStateDismissed, "./task check"},
		{"accepted-beats-dismissed", VerifyConfig{Test: "go test ./..."}, VerifyDetectCandidate{Command: "./task check", Dismissed: true}, true, wire.VerifySuggestionStateAccepted, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VerifyToDTO(proj, dir, tc.cfg, tc.detect, tc.hasDet)
			if got.SuggestionState != tc.want {
				t.Fatalf("suggestion_state = %q want %q", got.SuggestionState, tc.want)
			}
			if got.DetectedCommand != tc.wantCmd {
				t.Fatalf("detected_command = %q want %q", got.DetectedCommand, tc.wantCmd)
			}
		})
	}

	// Global scope carries no suggestion semantics.
	if got := VerifyToDTO(wire.SettingsScopeGlobal, "", VerifyConfig{}, VerifyDetectCandidate{}, false); got.SuggestionState != "" {
		t.Fatalf("global suggestion_state = %q want empty", got.SuggestionState)
	}
}
