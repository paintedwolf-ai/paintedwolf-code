package promptloop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

type partialFailingStreamLLM struct {
	err error
}

func (s partialFailingStreamLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, s.err
}

func (s partialFailingStreamLLM) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 2)
	ch <- modelcall.StreamChunk{Content: "partial answer"}
	ch <- modelcall.StreamChunk{Err: s.err, Done: true}
	close(ch)
	return ch, nil
}

type successStreamLLM struct{}

func (s successStreamLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "final answer"}, nil
}

func (s successStreamLLM) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 2)
	ch <- modelcall.StreamChunk{Content: "partial "}
	ch <- modelcall.StreamChunk{Content: "final answer", Done: true}
	close(ch)
	return ch, nil
}

func TestAssistantStreamSuccessClearsActiveProjectionBeforeCommit(t *testing.T) {
	var lifecycle []string
	loop := NewPromptLoopForTest(PromptLoopDeps{
		LLM: successStreamLLM{},
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
			lifecycle = append(lifecycle, "append")
			return nil
		},
		Streams: &testMessageStreams{
			cachelive: func(_, _, _ string, _ []string, _ int) {
				lifecycle = append(lifecycle, "live-cache")
			},
			cachereplay: func(_, _ string, _ []string) {
				lifecycle = append(lifecycle, "replay-cache")
			},
			project: func(_ context.Context, _ string, _ api.Message) error {
				lifecycle = append(lifecycle, "project")
				return nil
			},
			finish: func(_ context.Context, _ string) {
				lifecycle = append(lifecycle, "clear")
			},
		},

		UpdateMessage: func(_ context.Context, _, _ string, _ api.Message) error {
			lifecycle = append(lifecycle, "update")
			return nil
		},
	})
	sess := &api.Session{ID: "session-1", ProjectID: "project-1"}

	_, _, _, err := loop.runAssistantStreamTurn(
		context.Background(), sess.ID, sess, nil, &promptLoopTurnState{}, "worker", "go", 0, 3, false,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	clearIdx, updateIdx := -1, -1
	for i, step := range lifecycle {
		switch step {
		case "clear":
			if clearIdx == -1 {
				clearIdx = i
			}
		case "update":
			if updateIdx == -1 {
				updateIdx = i
			}
		}
	}
	if clearIdx == -1 || updateIdx == -1 {
		t.Fatalf("lifecycle = %v, want both clear and update", lifecycle)
	}
	if clearIdx > updateIdx {
		t.Fatalf("clear must precede the durable commit: lifecycle = %v", lifecycle)
	}
}

func TestAssistantStreamFailureClearsActiveProjectionAfterPartialContent(t *testing.T) {
	providerErr := errors.New("provider disconnected")
	var lifecycle []string
	loop := NewPromptLoopForTest(PromptLoopDeps{
		LLM: partialFailingStreamLLM{err: providerErr},
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
			lifecycle = append(lifecycle, "append")
			return nil
		},
		Streams: &testMessageStreams{
			cachelive: func(_, _, _ string, _ []string, _ int) {
				lifecycle = append(lifecycle, "live-cache")
			},
			project: func(_ context.Context, _ string, _ api.Message) error {
				lifecycle = append(lifecycle, "project")
				return nil
			},
			finish: func(_ context.Context, _ string) {
				lifecycle = append(lifecycle, "clear")
			},
		},
	})
	sess := &api.Session{ID: "session-1", ProjectID: "project-1"}

	_, _, _, err := loop.runAssistantStreamTurn(
		context.Background(), sess.ID, sess, nil, &promptLoopTurnState{}, "worker", "go", 0, 3, false,
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider disconnect", err)
	}

	want := []string{"live-cache", "append", "clear"}
	if len(lifecycle) != len(want) {
		t.Fatalf("lifecycle = %v, want %v", lifecycle, want)
	}
	for i := range want {
		if lifecycle[i] != want[i] {
			t.Fatalf("lifecycle = %v, want %v", lifecycle, want)
		}
	}
}

func TestCoordinatorStreamFailureWithdrawsPartialDraftAfterLiveFlush(t *testing.T) {
	providerErr := errors.New("provider disconnected")
	var lifecycle []string
	var withdrawn api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		LLM: partialFailingStreamLLM{err: providerErr},
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
			lifecycle = append(lifecycle, "append")
			return nil
		},
		Streams: &testMessageStreams{
			cachelive: func(_, _, _ string, _ []string, _ int) {
				lifecycle = append(lifecycle, "live-cache")
			},
			project: func(_ context.Context, _ string, _ api.Message) error {
				lifecycle = append(lifecycle, "project")
				return nil
			},
			finish: func(_ context.Context, _ string) {
				lifecycle = append(lifecycle, "clear")
			},
		},

		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			lifecycle = append(lifecycle, "withdraw")
			withdrawn = msg
			return nil
		},
	})
	sess := &api.Session{ID: "session-1", ProjectID: "project-1"}

	_, _, _, err := loop.runAssistantStreamTurn(
		context.Background(), sess.ID, sess, nil, &promptLoopTurnState{}, "coordinator", "go", 0, 3, false,
	)
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v, want provider disconnect", err)
	}
	if withdrawn.DraftStatus != api.DraftStatusWithdrawn || withdrawn.Content != "" {
		t.Fatalf("withdrawn draft = %+v", withdrawn)
	}
	want := []string{"live-cache", "append", "clear", "withdraw"}
	if len(lifecycle) != len(want) {
		t.Fatalf("lifecycle = %v, want %v", lifecycle, want)
	}
	for i := range want {
		if lifecycle[i] != want[i] {
			t.Fatalf("lifecycle = %v, want %v", lifecycle, want)
		}
	}
}

func TestOps8StreamDecisionPrecedesEveryContentDelivery(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "transform"
		if blocked {
			name = "block"
		}
		t.Run(name, func(t *testing.T) {
			reviewed := false
			updates, replays := 0, 0
			loop := NewPromptLoopForTest(PromptLoopDeps{
				LLM: successStreamLLM{},
				BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
					return history, nil
				},
				AppendMessages: func(_ context.Context, _ string, messages ...api.Message) error {
					for _, message := range messages {
						if message.Content != "" {
							t.Fatal("[OAR-OPS-8] placeholder published unreviewed content")
						}
					}
					return nil
				},
				EvaluateContentAnchor: func(_ context.Context, _ *api.Session, anchor string, segments []oar.ContentSegment, _ string, _ map[string]any) (*guidance.Refusal, bool, string, bool) {
					if anchor != oar.AnchorContentOutput {
						return nil, false, "", false
					}
					if len(segments) != 1 || !strings.Contains(segments[0].Content, "final answer") {
						t.Fatalf("[OAR-OPS-8] incomplete buffer: %#v", segments)
					}
					reviewed = true
					if blocked {
						return guidance.NewRefusal("WITHHELD", "withheld"), true, "", false
					}
					return nil, false, "reviewed replacement", true
				},
				Streams: &testMessageStreams{
					cachelive: func(_, _, content string, tokens []string, _ int) {
						if content != "" || len(tokens) != 0 {
							t.Fatal("[OAR-OPS-8] live cache exposed partial content")
						}
					},
					cachereplay: func(_, content string, tokens []string) {
						replays++
						if !reviewed || blocked || content != "reviewed replacement" || strings.Join(tokens, "") != "reviewed replacement" {
							t.Fatalf("[OAR-OPS-8] replay content=%q tokens=%v", content, tokens)
						}
					},
					project: func(_ context.Context, _ string, message api.Message) error {
						if message.Content != "" {
							t.Fatal("[OAR-OPS-8] live projection exposed content")
						}
						return nil
					},
					finish: func(context.Context, string) {},
				},
				UpdateMessage: func(_ context.Context, _, _ string, message api.Message) error {
					updates++
					if !reviewed || blocked || message.Content != "reviewed replacement" {
						t.Fatalf("[OAR-OPS-8] persisted content %q", message.Content)
					}
					return nil
				},
			})
			sess := &api.Session{ID: "buffer-test", ParentSessionID: "parent"}
			_, _, _, err := loop.runAssistantStreamTurn(t.Context(), sess.ID, sess, nil, &promptLoopTurnState{}, "worker", "go", 0, 3, false)
			if blocked {
				if err == nil || updates != 0 || replays != 0 {
					t.Fatalf("[OAR-OPS-8] blocked delivery: err=%v updates=%d replays=%d", err, updates, replays)
				}
			} else if err != nil || updates != 1 || replays != 1 {
				t.Fatalf("[OAR-OPS-8] transformed delivery: err=%v updates=%d replays=%d", err, updates, replays)
			}
		})
	}
}
