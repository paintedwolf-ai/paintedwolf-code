package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestStreamPolicyFreshnessIsIndependentOfJournalPreparation(t *testing.T) {
	for _, mode := range []string{"file", "stream", "batch"} {
		for _, timing := range []string{"review", "intent"} {
			t.Run(mode+"/"+timing, func(t *testing.T) {
				ctx, tc, change, stale := policyFenceFixture(t)
				journal := &policyFenceJournal{}
				tc.Source.SourceMutations = journal
				if timing == "intent" {
					journal.prepared = change
				}
				count := 1
				if mode == "batch" {
					count = 2
				}
				reviews := 0
				tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
					reviews++
					if timing == "review" && reviews == count {
						change()
					}
					return nil
				}
				var stages []*agentStreamStage
				var err error
				filePath := filepath.Join(tc.Source.Roots[0].Path, "file.txt")
				if mode == "file" {
					err = applyAgentFile(ctx, tc, testMutationTarget(filePath), []byte("reviewed bytes"), nil, "")
				}
				for i := 0; mode != "file" && i < count; i++ {
					path := filepath.Join(tc.Source.Roots[0].Path, string(rune('a'+i))+".txt")
					stage, _, err := stageAgentStream(ctx, tc, agentStreamRequest{
						Target: testMutationTarget(path), Source: strings.NewReader("reviewed bytes"), KnownAbsent: true,
					})
					if err != nil {
						t.Fatal(err)
					}
					defer stage.Close()
					stages = append(stages, stage)
				}
				if mode == "batch" {
					_, pending, publishErr := publishAgentStreams(ctx, tc, nil, stages)
					err = publishErr
					if pending != nil {
						pending.Close()
					}
				} else if mode == "stream" {
					_, err = stages[0].Commit(ctx, tc)
				}
				if !errors.Is(err, stale) {
					t.Fatalf("publication = %v, want stale policy", err)
				}
				paths := []string{filePath}
				for _, stage := range stages {
					paths = append(paths, stage.request.Target.Abs)
				}
				for _, path := range paths {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("destination published despite stale policy: %v", err)
					}
				}
				wantIntents := 0
				if timing == "intent" {
					wantIntents = 1
				}
				if journal.intents != wantIntents || journal.committed != 0 {
					t.Fatalf("journal intents=%d committed=%d", journal.intents, journal.committed)
				}
				if timing == "intent" && !errors.Is(journal.settled, stale) {
					t.Fatalf("uncommitted intent settlement = %v", journal.settled)
				}
			})
		}
	}
}

func TestRecoveryRemovalRechecksPolicyAfterEveryReview(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "recovery retry"}[retry], func(t *testing.T) {
			ctx, tc, change, stale := policyFenceFixture(t)
			path := filepath.Join(tc.Source.Roots[0].Path, "file.txt")
			before := []byte("preserved bytes")
			if err := os.WriteFile(path, before, 0o644); err != nil {
				t.Fatal(err)
			}
			journal := &policyFenceJournal{unavailable: retry}
			tc.Source.SourceMutations = journal
			reviews := 0
			tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
				reviews++
				if !retry || changes[0].RecoveryLoss {
					change()
				}
				return nil
			}
			err := removeAgentPath(ctx, tc, testMutationTarget(path), nil)
			if !errors.Is(err, stale) {
				t.Fatalf("removal = %v, want stale policy", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(before) {
				t.Fatalf("reviewed file lost: %q, %v", got, err)
			}
			wantCalls, wantReviews := 0, 1
			if retry {
				wantCalls, wantReviews = 1, 2
			}
			if journal.removals != wantCalls || reviews != wantReviews {
				t.Fatalf("removal attempts=%d reviews=%d", journal.removals, reviews)
			}
		})
	}
}

func policyFenceFixture(t *testing.T) (context.Context, tools.ToolContext, func(), error) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("delivered policy")
	stale := errors.New("policy changed after model request")
	ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
		for _, target := range targets {
			if target.RootID != "r1" || !target.Mutation {
				t.Fatalf("incorrect mutation jurisdiction: %+v", target)
			}
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(body) != "delivered policy" {
			return stale
		}
		return nil
	})
	tc := nativefixture.Context(root)
	tc.Identity.ProjectID = "p1"
	return ctx, tc, func() { write("new policy") }, stale
}

type policyFenceJournal struct {
	sourceeffect.Journal
	prepared    func()
	intents     int
	committed   int
	settled     error
	removals    int
	unavailable bool
}

func (j *policyFenceJournal) PrepareEffect(context.Context, sourceeffect.Plan) (sourceeffect.Pending, error) {
	j.intents++
	if j.prepared != nil {
		j.prepared()
	}
	return &policyFencePending{journal: j}, nil
}

func (j *policyFenceJournal) PrepareEffects(_ context.Context, _ sourceeffect.Memory, plans []sourceeffect.Plan) (sourceeffect.PendingEffects, error) {
	j.intents++
	if j.prepared != nil {
		j.prepared()
	}
	return &policyFencePending{journal: j, count: len(plans)}, nil
}

func (*policyFenceJournal) CanMoveAside(context.Context, fseffect.Location) (bool, error) {
	return true, nil
}

func (*policyFenceJournal) InspectRemoval(context.Context, fseffect.Location, sourceeffect.Memory) (sourceeffect.RemovalSnapshot, error) {
	return sourceeffect.RemovalSnapshot{Bytes: int64(len("preserved bytes")), Files: 1}, nil
}

func (j *policyFenceJournal) RemoveEntry(_ context.Context, removal sourceeffect.Removal) (string, error) {
	j.removals++
	if j.unavailable {
		return "", &sourceeffect.RecoveryUnavailableError{Cause: errors.New("recovery unavailable")}
	}
	return "removed", os.Remove(filepath.Join(removal.RootPath, removal.Record.Path))
}

type policyFencePending struct {
	journal *policyFenceJournal
	count   int
}

func (*policyFencePending) ID() string { return "intent" }
func (p *policyFencePending) IDs() []string {
	ids := make([]string, p.count)
	for i := range ids {
		ids[i] = "intent"
	}
	return ids
}
func (p *policyFencePending) Finish(_ context.Context, err error) error {
	p.journal.settled = err
	if err == nil {
		p.journal.committed++
	}
	return err
}
func (p *policyFencePending) Record(_ context.Context, completions []sourceeffect.Completion) error {
	for _, completion := range completions {
		if completion.Result.Publication.Committed() {
			p.journal.committed++
		}
		if completion.Err != nil {
			p.journal.settled = completion.Err
		}
	}
	return nil
}
func (*policyFencePending) Close() {}
