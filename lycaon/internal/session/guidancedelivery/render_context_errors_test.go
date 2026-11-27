package guidancedelivery

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubSessions struct {
	sess *api.Session
	err  error
}

func (s stubSessions) Get(context.Context, string) (*api.Session, error) {
	return s.sess, s.err
}

type stubTranscriptStore struct {
	msgs []api.Message
	err  error
}

func (s stubTranscriptStore) GetMessage(context.Context, string, string) (api.Message, error) {
	return api.Message{}, nil
}

func (s stubTranscriptStore) GetMessages(context.Context, string) ([]api.Message, error) {
	return s.msgs, s.err
}

type stubCycleState struct{}

func (stubCycleState) ForSession(context.Context, *api.Session) surface.ImplementSessionState {
	return surface.ImplementSessionState{}
}

type stubFrameSource struct {
	frame inject.CoordinatorTurnFrame
	err   error
}

func (s stubFrameSource) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	return s.frame, s.err
}

func newRenderContextErrorHarness(t *testing.T, sessions Sessions, transcripts TranscriptStore, frame inject.CoordinatorTurnFrameSource, reader RequestReader) (*Service, *recordedSignals) {
	t.Helper()
	kicks := &kick.KickEngine{}
	kicks.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	var signals recordedSignals
	svc := &Service{
		sessionStore:    sessions,
		transcriptStore: transcripts,
		transcript:      &signals,
		state:           stubCycleState{},
		frame:           frame,
		requestReader:   reader,
	}
	svc.Bind(kicks, anchor.NewBus(kicks))
	testutil.FailErr(t, "install anchor registry", svc.InstallAnchorRegistry())
	svc.Emit(t.Context(), "sess-1", anchor.ProcessFinished, anchor.Envelope{
		Subject: "job-1", CommandCompletionDigest: "handle=job-1",
	})
	svc.Emit(t.Context(), "sess-1", anchor.PhaseEntered, anchor.Envelope{
		PhaseID: "review",
	})
	return svc, &signals
}

func TestRenderContextErrorsPropagateAndSkipCleanly(t *testing.T) {
	boom := errors.New("backend unavailable")
	validSess := &api.Session{ID: "sess-1", ProjectID: "proj-1"}
	okSessions := stubSessions{sess: validSess}
	okTranscript := stubTranscriptStore{}
	okFrame := stubFrameSource{}
	okReader := func(context.Context, *api.Session, []api.Message) (prompts.Reader, error) {
		return prompts.Reader{}, nil
	}

	cases := []struct {
		name        string
		sessions    Sessions
		transcripts TranscriptStore
		frame       inject.CoordinatorTurnFrameSource
		reader      RequestReader
		wantErr     error
	}{
		{
			name:        "session store failure returns error",
			sessions:    stubSessions{err: boom},
			transcripts: okTranscript,
			frame:       okFrame,
			reader:      okReader,
			wantErr:     boom,
		},
		{
			name:        "transcript store failure returns error",
			sessions:    okSessions,
			transcripts: stubTranscriptStore{err: boom},
			frame:       okFrame,
			reader:      okReader,
			wantErr:     boom,
		},
		{
			name:        "frame builder failure returns error",
			sessions:    okSessions,
			transcripts: okTranscript,
			frame:       stubFrameSource{err: boom},
			reader:      okReader,
			wantErr:     boom,
		},
		{
			name:        "request reader failure returns error",
			sessions:    okSessions,
			transcripts: okTranscript,
			frame:       okFrame,
			reader: func(context.Context, *api.Session, []api.Message) (prompts.Reader, error) {
				return prompts.Reader{}, boom
			},
			wantErr: boom,
		},
		{
			name:        "session not found skips without error",
			sessions:    stubSessions{err: store.ErrSessionNotFound},
			transcripts: okTranscript,
			frame:       okFrame,
			reader:      okReader,
			wantErr:     nil,
		},
		{
			name:        "not runnable frame skips without error",
			sessions:    okSessions,
			transcripts: okTranscript,
			frame: stubFrameSource{err: &runstate.NotRunnableError{
				RunID: "run-1", Status: api.WorkflowRunStatusPaused, Reason: runstate.ReviewBlockedReason,
			}},
			reader:  okReader,
			wantErr: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, signals := newRenderContextErrorHarness(t, tc.sessions, tc.transcripts, tc.frame, tc.reader)

			err := svc.DeliverPending(t.Context(), "sess-1")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("DeliverPending err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("DeliverPending err = %v, want nil", err)
			}
			if len(*signals) != 0 {
				t.Fatalf("DeliverPending appended messages: %v", *signals)
			}

			msgs, err := svc.TakePhase(t.Context(), "sess-1")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("TakePhase err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("TakePhase err = %v, want nil", err)
			}
			if len(msgs) != 0 || len(*signals) != 0 {
				t.Fatalf("TakePhase delivered msgs=%v signals=%v, want none", msgs, *signals)
			}
		})
	}
}
