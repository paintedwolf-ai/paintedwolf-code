package promptloop

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptObservationAcknowledgesOnlyDeliveredFrames(t *testing.T) {
	for _, hostTurn := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			name := map[bool]string{false: "user", true: "host"}[hostTurn] + "/" + map[bool]string{false: "success", true: "failure"}[fail]
			t.Run(name, func(t *testing.T) {
				memory := store.NewMemory()
				source := &countingCoordinatorFrameSource{}
				deps := StoreDeps(memory)
				deps.CoordinatorFrame = source
				deps.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "done"}}})
				if fail {
					deps.LLM = &immediateFailingStreamLLM{err: errors.New("model unavailable")}
				}
				captures, acknowledgements := 0, 0
				deps.ObservePrompt = func(string) func(inject.CoordinatorTurnFrame) {
					captures++
					if source.calls != captures-1 {
						t.Errorf("capture happened after workflow read: reads=%d captures=%d", source.calls, captures)
					}
					return func(frame inject.CoordinatorTurnFrame) {
						acknowledgements++
						if frame.WorkflowRevision != int64(acknowledgements) {
							t.Errorf("acknowledged frame=%+v", frame)
						}
					}
				}
				deps.BuildMessages = func(_ context.Context, _ *api.Session, history []api.Message, frame *inject.CoordinatorTurnFrame) ([]api.Message, error) {
					if acknowledgements >= captures {
						t.Error("acknowledged before model execution")
					}
					return history, nil
				}
				sess, err := memory.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
				testutil.FailErr(t, "create session", err)
				_, err = NewPromptLoopForTest(deps).Run(t.Context(), PromptRunInput{
					SessionID: sess.ID, Session: sess, ProfileID: "coordinator", HostTurn: hostTurn,
					History: []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
				})
				if fail {
					if err == nil || acknowledgements != 0 {
						t.Fatalf("failed request: error=%v acknowledgements=%d", err, acknowledgements)
					}
				} else {
					testutil.FailErr(t, "run prompt", err)
					if acknowledgements == 0 || acknowledgements != captures {
						t.Fatalf("captures=%d acknowledgements=%d", captures, acknowledgements)
					}
				}
			})
		}
	}
}
