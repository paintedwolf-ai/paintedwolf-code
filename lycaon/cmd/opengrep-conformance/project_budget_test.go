package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectBudgetIsTheGivenTimeoutOnEveryHost(t *testing.T) {
	t.Parallel()
	budget, err := resolveProjectBudget(defaultProjectTimeout)
	testutil.FailErr(t, "resolve project budget", err)
	before := time.Now()
	ctx, cancel := budget.context(context.Background())
	defer cancel()
	after := time.Now()
	deadline, exists := ctx.Deadline()
	if !exists || deadline.Before(before.Add(defaultProjectTimeout)) || deadline.After(after.Add(defaultProjectTimeout)) {
		t.Fatalf("execution deadline does not use the resolved budget: %v", deadline)
	}
	for _, duration := range []time.Duration{0, -time.Second} {
		if _, err := resolveProjectBudget(duration); err == nil {
			t.Fatalf("invalid project budget accepted: %s", duration)
		}
	}
}

func TestProjectBudgetEvidenceRecordsResolvedDeadline(t *testing.T) {
	t.Parallel()
	budget, err := resolveProjectBudget(defaultProjectTimeout)
	testutil.FailErr(t, "resolve evidence budget", err)
	root := t.TempDir()
	binary, corpus := filepath.Join(root, "scanner"), filepath.Join(root, "corpus.yaml")
	testutil.FailErr(t, "write scanner identity fixture", os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	testutil.FailErr(t, "write corpus identity fixture", os.WriteFile(corpus, []byte("projects: []\n"), 0o600))
	var output bytes.Buffer
	testutil.FailErr(t, "write evaluation identity", writeEvaluationIdentity(json.NewEncoder(&output), binary, nil, corpus, false, budget))
	var identity map[string]string
	testutil.FailErr(t, "decode evaluation identity", json.Unmarshal(output.Bytes(), &identity))
	if identity["project_deadline"] != defaultProjectTimeout.String() {
		t.Fatalf("evaluation evidence changed the resolved budget: %+v", identity)
	}
	parent, stop := context.WithCancel(t.Context())
	stop()
	ctx, cancel := budget.context(parent)
	defer cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("project budget discarded parent cancellation")
	}
}
