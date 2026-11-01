package history

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type failedMeasurement struct{ err error }

func (f failedMeasurement) RequestBudget(context.Context, string, modelcall.CompletionRequest) modelcall.RequestBudget {
	return modelcall.RequestBudget{Err: f.err}
}

func TestRequestFitPreservesMeasurementFailure(t *testing.T) {
	memory := store.NewMemory()
	session, err := memory.Create(t.Context(), api.CreateSessionRequest{ProviderID: "local", Model: "e4b"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	service := &Service{store: memory, Compactor: compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), nil)}
	failure := errors.New("fixture tokenizer unavailable")
	request := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}}
	_, err = service.FitRequest(t.Context(), session, nil, request, failedMeasurement{failure}, nil)
	if !errors.Is(err, failure) {
		t.Fatalf("measurement failure became an estimated fit: %v", err)
	}
}

type exactMeasurement struct{}

func (exactMeasurement) RequestBudget(context.Context, string, modelcall.CompletionRequest) modelcall.RequestBudget {
	return modelcall.RequestBudget{Window: 512, Input: 100, Output: 32, Exact: true}
}

func TestExactMeasurementDoesNotApplyEstimatedCalibration(t *testing.T) {
	service := &Service{}
	session := &api.Session{ID: "measured", ProviderID: "local", Model: "e4b"}
	request := modelcall.CompletionRequest{Model: session.Model, MaxTokens: 32, Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}}
	estimate := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(request.Messages))
	service.ObserveTokens(session.ID, session.ProviderID, session.Model, 10000, estimate)
	if !service.Calibration(t.Context(), session).Calibrated() {
		t.Fatal("fixture has no observed calibration")
	}
	fitted, err := service.FitRequest(t.Context(), session, request.Messages, request, exactMeasurement{}, nil)
	testutil.FailErr(t, "fit measured request", err)
	if len(fitted.Messages) != 1 || fitted.Messages[0].Content != "Question" || fitted.MaxTokens != 32 {
		t.Fatalf("estimated calibration changed exact fit: %+v", fitted)
	}
}
