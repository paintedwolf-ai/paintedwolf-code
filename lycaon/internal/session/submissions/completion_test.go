package submissions

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type completionStore interface {
	Store
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	InterruptRunningPromptSubmissionsBySession(context.Context, string) error
}

func TestStoppedReceiptWinsLateCompletion(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) completionStore
	}{
		{"memory", func(*testing.T) completionStore { return store.NewMemory() }},
		{"sql", func(t *testing.T) completionStore {
			db := testdbfixture.Open(t, "completion.db")
			testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
			return store.NewSQL(db)
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			for _, runErr := range []error{nil, context.Canceled, lifecycle.ErrStopping, errors.New("late provider failure")} {
				row, _, err := st.PutPromptSubmission(t.Context(), store.PromptSubmission{
					ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID,
					InputDigest: "digest", InputJSON: `{}`, Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
				})
				testutil.FailErr(t, "admit prompt", err)
				claimed, won, err := st.ClaimPromptSubmission(t.Context(), row.ID)
				testutil.FailErr(t, "claim prompt", err)
				if !won {
					t.Fatal("prompt was not claimed")
				}
				testutil.FailErr(t, "stop session receipts", st.InterruptRunningPromptSubmissionsBySession(t.Context(), sess.ID))
				before, err := st.GetPromptSubmission(t.Context(), row.ID)
				testutil.FailErr(t, "read stopped receipt", err)
				service := &Service{store: st, SubmissionState: &submissionstate.Service{}}
				if err := service.finishSubmission(t.Context(), claimed, nil, runErr); !errors.Is(err, lifecycle.ErrStopping) {
					t.Fatalf("late finish = %v, want a stop", err)
				}
				after, err := st.GetPromptSubmission(t.Context(), row.ID)
				testutil.FailErr(t, "read settled receipt", err)
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("stopped receipt changed: before=%+v after=%+v", before, after)
				}
				claimed.ClaimToken = "stale"
				if err := service.finishSubmission(t.Context(), claimed, nil, nil); err == nil || errors.Is(err, lifecycle.ErrStopping) {
					t.Fatalf("stale claim was mistaken for its own stop: %v", err)
				}
			}
		})
	}
}

type completionFailureStore struct {
	Store
	finishErr error
	readErr   error
	row       *store.PromptSubmission
}

func (s completionFailureStore) FinishPromptSubmission(context.Context, string, string, store.PromptSubmissionStatus, string, store.PromptSubmissionFailure) error {
	return s.finishErr
}
func (s completionFailureStore) GetPromptSubmission(context.Context, string) (*store.PromptSubmission, error) {
	return s.row, s.readErr
}

func TestCompletionPreservesUnconfirmedFailure(t *testing.T) {
	failure := errors.New("write failed")
	for _, current := range []*store.PromptSubmission{nil, {Status: store.PromptSubmissionRunning, ClaimToken: "claim"}, {Status: store.PromptSubmissionComplete, ClaimToken: "claim"}} {
		st := completionFailureStore{finishErr: failure, row: current}
		service := &Service{store: st, SubmissionState: &submissionstate.Service{}}
		if err := service.finishSubmission(t.Context(), &store.PromptSubmission{ID: "id", ClaimToken: "claim"}, nil, nil); !errors.Is(err, failure) {
			t.Fatalf("lost completion failure: %v", err)
		}
	}
	st := completionFailureStore{finishErr: failure, readErr: errors.New("read failed"), row: &store.PromptSubmission{Status: store.PromptSubmissionInterrupted, ClaimToken: "claim"}}
	service := &Service{store: st, SubmissionState: &submissionstate.Service{}}
	if err := service.finishSubmission(t.Context(), &store.PromptSubmission{ID: "id", ClaimToken: "claim"}, nil, nil); !errors.Is(err, failure) {
		t.Fatalf("unreadable receipt masked completion failure: %v", err)
	}
}
