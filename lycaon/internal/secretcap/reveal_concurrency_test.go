package secretcap

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRevealRecordsItsVersionBeforeReplacement(t *testing.T) {
	service, _, _ := testService(t)
	person := testOwner(t, service)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: person, OperationID: "reveal-concurrent",
		Name: "deploy token", Purpose: "deployment", Value: "original-deploy-token",
	})
	testutil.FailErr(t, "create capability", err)
	key := configureRevealKey(t, service)
	challenge, err := service.Reveals.BeginReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference, "main", person)
	testutil.FailErr(t, "begin reveal", err)
	proof := signedProof(key, challenge, presence.AuthenticatorMacOS)
	stamp := service.repository.now()
	reachedAudit, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	service.repository.now = func() time.Time {
		if calls.Add(1) == 1 {
			close(reachedAudit)
			<-resume
		}
		return stamp
	}
	type revealed struct {
		result RevealResult
		err    error
	}
	result := make(chan revealed, 1)
	go func() {
		value, err := service.Reveals.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, meta.Reference, person, proof)
		result <- revealed{value, err}
	}()
	select {
	case <-reachedAudit:
	case got := <-result:
		testutil.FailErr(t, "complete reveal before audit barrier", got.err)
		t.Fatal("reveal bypassed the audit barrier")
	}
	// The audit and competing mutations share one serialization boundary.
	if service.repository.mutationMu.TryLock() {
		service.repository.mutationMu.Unlock()
		close(resume)
		<-result
		t.Fatal("reveal did not serialize its version check and audit with mutations")
	}
	replaced := make(chan error, 1)
	go func() {
		_, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "replacement-deploy-token"})
		replaced <- err
	}()
	close(resume)
	got := <-result
	testutil.FailErr(t, "complete reveal", got.err)
	testutil.FailErr(t, "replace after reveal", <-replaced)
	if got.result.Version != 1 || got.result.Value.Value() != "original-deploy-token" {
		t.Fatal("reveal did not return its verified version")
	}
	var version int64
	testutil.FailErr(t, "read reveal audit", service.repository.handle.QueryRowContext(t.Context(), "SELECT version FROM managed_secret_reveals WHERE id = ?", challenge.ID).Scan(&version))
	if version != 1 {
		t.Fatalf("reveal audit version = %d", version)
	}
	current, err := service.Describe(t.Context(), testdbseed.DefaultProjectID, "", meta.Reference)
	testutil.FailErr(t, "describe replacement", err)
	if current.Version != 2 {
		t.Fatalf("current version = %d", current.Version)
	}
}
