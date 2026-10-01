package worker

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestSQLStoreFailureJSONRoundtrip(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := NewSQLStore(sqlDB)
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	task := api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "code-reviewer",
		Prompt:          "fixture",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
		Status:          api.WorkerStatusPending,
	}
	testutil.FailErr(t, "store.InsertTask", store.InsertTask(context.Background(), task))

	claimed, err := store.ClaimNext(context.Background(), ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	}, 0, nil)
	testutil.FailErr(t, "store.ClaimNext", err)
	jobID := claimed.ID

	failure := &api.WorkerFailure{
		Code:            "worker_closeout_exhausted",
		Title:           "Worker could not finish",
		Message:         "The worker used every tool turn and both closing attempts.",
		SuggestedAction: "Retry with a narrower task() brief.",
	}
	stale := *claimed
	stale.ClaimToken = "stale"
	won, err := store.FailJob(context.Background(), &stale, "stale failure", failure)
	testutil.FailErr(t, "store.FailJob stale", err)
	if won {
		t.Fatal("stale claim failed the worker")
	}
	won, err = store.FailJob(
		context.Background(),
		claimed,
		"llm turn timed out: context deadline exceeded",
		failure,
	)
	testutil.FailErr(t, "store.FailJob", err)
	if !won {
		t.Fatal("FailJob lost the terminal CAS")
	}

	got, ok := store.getTask(context.Background(), jobID)
	if !ok {
		t.Fatal("getTask missing job")
	}
	if got.Status != api.WorkerStatusFailed {
		t.Fatalf("status = %q want failed", got.Status)
	}
	if got.Failure == nil {
		t.Fatal("Failure nil after reload")
	}
	if got.Failure.Code != failure.Code || got.Failure.Message != failure.Message {
		t.Fatalf("Failure = %+v want %+v", got.Failure, failure)
	}
}
