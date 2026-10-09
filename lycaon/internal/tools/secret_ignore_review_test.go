package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type reviewingSecretHITL struct {
	secretScreenHITL
	inspect func(hitl.CheckpointRequest)
}

func (m *reviewingSecretHITL) RequestCheckpoint(ctx context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.inspect(req)
	return m.secretScreenHITL.RequestCheckpoint(ctx, req)
}

func TestSecretIgnoreReviewNeverPersistsValueOrInstallsGrant(t *testing.T) {
	const value = "AKIAQYJK5TXV4NZR7SGB"
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "matcher", err)
	matcher.SetFingerprinter(fp)
	service := &projectignore.SecretService{Roots: func(context.Context, string) ([]projectignore.Root, error) {
		return []projectignore.Root{{ID: "root", Path: t.TempDir()}}, nil
	}}
	var token string
	mgr := &reviewingSecretHITL{secretScreenHITL: secretScreenHITL{status: hitl.DecisionStatusApproved}}
	mgr.inspect = func(req hitl.CheckpointRequest) {
		if req.SecretScreen == nil || req.SecretScreen.IgnoreCandidateID == "" {
			t.Fatal("missing contextual review")
		}
		token = req.SecretScreen.IgnoreCandidateID
		got, err := service.Reviews.Value("project", token)
		testutil.FailErr(t, "review live candidate", err)
		if got != value {
			t.Fatal("candidate changed exact bytes")
		}
		encoded, err := json.Marshal(req)
		testutil.FailErr(t, "encode checkpoint", err)
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatal("checkpoint persisted candidate plaintext")
		}
		for _, offer := range req.GrantOffers {
			if offer.Grant.Predicate.Category == "not_a_secret" {
				t.Fatal("classifier grant offered")
			}
		}
	}
	executor := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	executor.Approvals.SetCheckpointManager(mgr, nil)
	executor.Secrets.SetSecretMatcher(matcher)
	executor.Secrets.SetSecretIgnores(service)
	_, err = executor.Secrets.AskSecretScreen(t.Context(), secretmatch.Alert{SessionID: "session", ProjectID: "project", Surface: secretmatch.SurfaceCommand, DestinationID: "process", RuleID: "gitleaks:aws-access-token", RuleTitle: "AWS access key", Fingerprints: []secretmatch.SecretFingerprint{fp.Fingerprint(value)}, ReviewValue: value})
	testutil.FailErr(t, "resolve held request", err)
	if _, err := service.Reviews.Value("project", token); !errors.Is(err, projectignore.ErrReviewUnavailable) {
		t.Fatalf("completed review remained available: %v", err)
	}
}
