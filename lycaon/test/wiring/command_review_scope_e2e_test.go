package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A stored review rule names a mutating tool. Each command form reaches the review
// under its own identity, so a rule for it holds the change and a rule for another
// tool does not.
func TestReviewRuleScopedToAToolHoldsItsCommandForm(t *testing.T) {
	forms := []struct{ tool, other, file, seed, command string }{
		{"edit", "replace_lines", "a.txt", "old\n", "edit --path a.txt --old-string old --new-string new"},
		{"replace_lines", "edit", "a.txt", "old\n", "replace-lines --path a.txt --start 1 --end 1 --content new"},
		{"code_rewrite", "jq_edit", "a.go", "package a\n\nfunc Old() {}\n", "code-rewrite --pattern Old --rewrite New --path a.go"},
		{"jq_edit", "code_rewrite", "a.json", "{\"a\":1}\n", "jq-edit --path a.json --query '.a = 2'"},
	}
	for _, form := range forms {
		t.Run(form.tool+"/held", func(t *testing.T) {
			run := newReviewScopeRun(t, form.tool, form.file, form.seed, form.command)
			done := make(chan error, 1)
			go func() {
				_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "change it")
				done <- err
			}()
			kind := wire.CheckpointKindContentApply
			var card string
			testutil.WaitFor(t, func() bool {
				select {
				case err := <-done:
					testutil.FailErr(t, "prompt", err)
					t.Fatal("the turn ended without a content_apply card")
				default:
				}
				pending, err := run.h.Sessions.Checkpoints.ListPending(t.Context(), run.session, &kind)
				if err != nil || len(pending) == 0 {
					return false
				}
				card = pending[0].ID
				return true
			})
			if got := run.read(t); got != form.seed {
				t.Fatalf("file changed before the decision: %q", got)
			}
			_, err := run.h.Sessions.Checkpoints.ResolveCheckpoint(run.h.OwnerCtx(t, t.Context()), run.session, card, kind, nil, &hitl.ContentApplyResolution{
				Decision: wire.ContentApplyReject,
			})
			testutil.FailErr(t, "reject the change", err)
			testutil.FailErr(t, "prompt", <-done)
			if got := run.read(t); got != form.seed {
				t.Fatalf("rejected change landed: %q", got)
			}
		})
		t.Run(form.tool+"/other_tool_rule", func(t *testing.T) {
			run := newReviewScopeRun(t, form.other, form.file, form.seed, form.command)
			_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "change it")
			testutil.FailErr(t, "prompt", err)
			if got := run.read(t); got == form.seed {
				t.Fatalf("a rule for %s held %s", form.other, form.tool)
			}
		})
	}
}

// A rule with no tool holds every form, including one whose operation is not itself
// a content mutation.
func TestReviewRuleWithoutAToolHoldsAQueryPublication(t *testing.T) {
	run := newReviewScopeRun(t, "", "a.yaml", "a: 1\n", "yq -i '.a = 2' a.yaml")
	done := make(chan error, 1)
	go func() {
		_, err := run.h.Sessions.Manager.Submissions.Prompt(t.Context(), run.session, "change it")
		done <- err
	}()
	kind := wire.CheckpointKindContentApply
	var card string
	testutil.WaitFor(t, func() bool {
		select {
		case err := <-done:
			testutil.FailErr(t, "prompt", err)
			t.Fatal("the turn ended without a content_apply card")
		default:
		}
		pending, err := run.h.Sessions.Checkpoints.ListPending(t.Context(), run.session, &kind)
		if err != nil || len(pending) == 0 {
			return false
		}
		card = pending[0].ID
		return true
	})
	_, err := run.h.Sessions.Checkpoints.ResolveCheckpoint(run.h.OwnerCtx(t, t.Context()), run.session, card, kind, nil, &hitl.ContentApplyResolution{
		Decision: wire.ContentApplyReject,
	})
	testutil.FailErr(t, "reject the change", err)
	testutil.FailErr(t, "prompt", <-done)
	if got := run.read(t); got != "a: 1\n" {
		t.Fatalf("rejected change landed: %q", got)
	}
}

type reviewScopeRun struct {
	h       *Harness
	session string
	path    string
}

func (r *reviewScopeRun) read(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(r.path)
	testutil.FailErr(t, "read file", err)
	return string(body)
}

func newReviewScopeRun(t *testing.T, ruleTool, file, seed, command string) *reviewScopeRun {
	t.Helper()
	dir := t.TempDir()
	overlay := filepath.Join(dir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay", os.MkdirAll(overlay, 0o755))
	rule := "review_paths:\n  - path: '**'\n"
	if ruleTool != "" {
		rule += "    tool: " + ruleTool + "\n"
	}
	testutil.FailErr(t, "write review rule", os.WriteFile(filepath.Join(overlay, "review.yaml"), []byte(rule), 0o600))
	path := filepath.Join(dir, file)
	testutil.FailErr(t, "seed file", os.WriteFile(path, []byte(seed), 0o644))
	script := &citableScriptLLM{steps: []citableStep{{command: command}}}
	h := BuildForTest(t, WithLLMClient(script))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	h.SeedProgress(t, t.Context(), sess.ID)
	return &reviewScopeRun{h: h, session: sess.ID, path: path}
}
