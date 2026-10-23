package conformance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestReferenceComparisonIncludesAllObservableResults(t *testing.T) {
	ref := fixtureSnapshot{Status: 1, Stdout: "output\n", Stderr: "diagnostic\n", Files: map[string]*string{"result": strPtr("bytes\n")}}
	cases := []struct {
		name   string
		change func(*runtimeObservation)
		match  bool
	}{
		{"equal", func(*runtimeObservation) {}, true},
		{"stdout", func(o *runtimeObservation) { o.Outcome.Stdout += "hidden" }, false},
		{"stderr", func(o *runtimeObservation) { o.Outcome.Stderr = "" }, false},
		{"status", func(o *runtimeObservation) { o.Outcome.Status = 0 }, false},
		{"unsettled", func(o *runtimeObservation) { o.Outcome.State = "background" }, false},
		{"file bytes", func(o *runtimeObservation) { o.Files["result"] = strPtr("other\n") }, false},
		{"file absent", func(o *runtimeObservation) { o.Files["result"] = nil }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := runtimeObservation{
				Outcome: command.ProgramOutcome{State: "completed", Status: ref.Status, Stdout: ref.Stdout, Stderr: ref.Stderr},
				Files:   map[string]*string{"result": strPtr("bytes\n")},
			}
			tc.change(&obs)
			if got := matchesSnapshot(obs, ref); got != tc.match {
				t.Fatalf("reference comparison = %v, want %v: %+v", got, tc.match, obs)
			}
		})
	}
}
