package session_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

// knownWorkerEnvelopeStates is the complete completion-state vocabulary.
var knownWorkerEnvelopeStates = []string{
	"complete",
	"partial",
	"canceled",
	"open",
	"needs_decision",
	"held",
	"failed",
}

// deliberateStopStates end a leg without blocking wrap-up.
var deliberateStopStates = map[string]struct{}{
	string(api.WorkerSummaryStatusCanceled): {},
}

var repairOnlyStates = map[string]struct{}{
	string(api.WorkerSummaryStatusFailed): {},
}

func envelopeMessageForState(state string) api.Message {
	jobID := "j-" + state
	childSessionID := "child-" + state
	status := api.WorkerSummaryStatus(state)
	envelope := session.FormatWorkerCompletionEnvelope(session.WorkerCompletionEnvelope{
		JobID:          jobID,
		ChildSessionID: childSessionID,
		AgentType:      "implementer",
		State:          state,
	})
	return api.Message{
		Role: api.MessageRoleAssistant,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:       jobID,
			ChildSessionID: childSessionID,
			AgentType:      "implementer",
			Status:         status,
			Envelope:       envelope,
		},
	}
}

func TestWorkerEnvelopeStateVocabularyFullyClassified(t *testing.T) {
	for _, state := range knownWorkerEnvelopeStates {
		msg := envelopeMessageForState(state)
		history := []api.Message{{Role: api.MessageRoleUser, Content: "do work"}, msg}
		since := api.UserIntentBoundary(history)

		blocks := session.OpenWorkerEnvelopeSince(history, since)
		finishedLeg := surface.TerminalWorkerCompletionMessage(msg)
		_, deliberateStop := deliberateStopStates[state]
		_, repairOnly := repairOnlyStates[state]

		buckets := 0
		for _, in := range []bool{blocks, finishedLeg, deliberateStop, repairOnly} {
			if in {
				buckets++
			}
		}
		if buckets != 1 {
			t.Fatalf("state %q lands in %d wrap-up buckets, want exactly 1 (blocks=%v, finishedLeg=%v, deliberateStop=%v, repairOnly=%v)",
				state, buckets, blocks, finishedLeg, deliberateStop, repairOnly)
		}
	}
}

var envelopeStateLiteralRE = regexp.MustCompile(`\bstate=["']([a-z_]+)["']`)

func TestWorkerEnvelopeStateLiteralsAreKnown(t *testing.T) {
	known := make(map[string]struct{}, len(knownWorkerEnvelopeStates))
	for _, s := range knownWorkerEnvelopeStates {
		known[s] = struct{}{}
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	lycaonRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	scanDirs := []string{
		filepath.Join(lycaonRoot, "internal", "session"),
		filepath.Join(lycaonRoot, "internal", "worker"),
		filepath.Join(lycaonRoot, "internal", "coordinator", "surface"),
	}

	var violations []string
	for _, dir := range scanDirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(lycaonRoot, path)
			for _, m := range envelopeStateLiteralRE.FindAllStringSubmatch(string(data), -1) {
				if _, ok := known[m[1]]; !ok {
					violations = append(violations, rel+`: state="`+m[1]+`" not in knownWorkerEnvelopeStates`)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("unclassified worker-envelope state literals:\n  %s", strings.Join(violations, "\n  "))
	}
}
