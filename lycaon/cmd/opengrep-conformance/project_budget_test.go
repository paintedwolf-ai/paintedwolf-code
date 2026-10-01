package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectBudgetAppliesContentionScaleOnce(t *testing.T) {
	t.Parallel()
	budget, err := resolveProjectBudget(defaultProjectTimeout, 4)
	testutil.FailErr(t, "resolve project budget", err)
	if budget.Effective != 40*time.Minute {
		t.Fatalf("scaled project budget = %s", budget.Effective)
	}
	before := time.Now()
	ctx, cancel := budget.context(context.Background())
	defer cancel()
	after := time.Now()
	deadline, exists := ctx.Deadline()
	if !exists || deadline.Before(before.Add(budget.Effective)) || deadline.After(after.Add(budget.Effective)) {
		t.Fatalf("execution deadline does not use the resolved budget: %v", deadline)
	}
	custom, err := resolveProjectBudget(30*time.Minute, 2)
	testutil.FailErr(t, "resolve custom project budget", err)
	if custom.Effective != time.Hour {
		t.Fatalf("custom project budget = %s", custom.Effective)
	}
}

func TestProjectBudgetRejectsInvalidOrOverflowingDurations(t *testing.T) {
	t.Parallel()
	for _, duration := range []time.Duration{0, -time.Second, time.Duration(math.MaxInt64)} {
		if _, err := resolveProjectBudget(duration, 4); err == nil {
			t.Fatalf("invalid project budget accepted: %s", duration)
		}
	}
}

func TestProjectBudgetEvidenceRecordsResolvedDeadline(t *testing.T) {
	t.Parallel()
	budget, err := resolveProjectBudget(defaultProjectTimeout, 4)
	testutil.FailErr(t, "resolve evidence budget", err)
	root := t.TempDir()
	binary, corpus := filepath.Join(root, "scanner"), filepath.Join(root, "corpus.yaml")
	testutil.FailErr(t, "write scanner identity fixture", os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	testutil.FailErr(t, "write corpus identity fixture", os.WriteFile(corpus, []byte("projects: []\n"), 0o600))
	var output bytes.Buffer
	testutil.FailErr(t, "write evaluation identity", writeEvaluationIdentity(json.NewEncoder(&output), binary, nil, corpus, false, budget))
	var identity map[string]string
	testutil.FailErr(t, "decode evaluation identity", json.Unmarshal(output.Bytes(), &identity))
	if identity["test_deadline"] != budget.Effective.String() || identity["test_base_deadline"] != budget.Base.String() || identity["test_timeout_scale"] != "4" {
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
