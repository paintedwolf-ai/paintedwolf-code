//go:build referenceuserland

package conformance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

func TestLiveGeneratedDifferential(t *testing.T) {
	seeds, count, err := generatedSelection()
	testutil.FailErr(t, "select generated programs", err)
	var programs []commandconformance.GeneratedProgram
	var cases []commandconformance.Case
	for _, seed := range seeds {
		batch, err := commandconformance.Generate(seed, count)
		testutil.FailErr(t, "generate programs", err)
		programs = append(programs, batch...)
		for _, p := range batch {
			cases = append(cases, p.Case())
		}
	}
	testutil.FailErr(t, "validate generated cases", commandconformance.Validate(cases))
	references, err := referenceuserland.NewRunner(t.Context())
	testutil.FailErr(t, "open reference userlands", err)
	t.Cleanup(func() { _ = references.Close() })
	runner := newConformanceRunner(t, gate.PostureLight, true)
	live, err := commandconformance.Capture(t.Context(), references, cases, runner.seed)
	testutil.FailErr(t, "capture generated references", err)
	mismatches := 0
	for _, p := range programs {
		t.Run(fmt.Sprintf("seed-%d/program-%d", p.Seed, p.Ordinal), func(t *testing.T) {
			c := p.Case()
			obs := runner.runCase(t, c)
			defect := generatedMismatch(obs, live[c.ID])
			if defect.Kind == "" {
				return
			}
			mismatches++
			lastObs, lastRefs := obs, live[c.ID]
			type trialObservation struct {
				native     runtimeObservation
				references map[referenceuserland.ID]commandconformance.Snapshot
			}
			observed := map[string]trialObservation{c.Program: {obs, live[c.ID]}}
			result := commandconformance.ShrinkResult{Program: p}
			if obs.TopLevelReject == "" {
				result, err = commandconformance.Shrink(t.Context(), p, 128, func(ctx context.Context, trials []commandconformance.Case) ([]bool, error) {
					captured, err := commandconformance.Capture(ctx, references, trials, runner.seed)
					if err != nil {
						return nil, err
					}
					kept := make([]bool, len(trials))
					for i, trial := range trials {
						current := runner.runCase(t, trial)
						observed[trial.Program] = trialObservation{current, captured[trial.ID]}
						kept[i] = reflect.DeepEqual(generatedMismatch(current, captured[trial.ID]), defect)
					}
					return kept, nil
				})
			}
			if selected, ok := observed[result.Program.Case().Program]; ok {
				lastObs, lastRefs = selected.native, selected.references
			}

			testutil.FailErr(t, "shrink generated mismatch", err)
			report, err := json.MarshalIndent(struct {
				Seed       uint64
				Ordinal    int
				Original   string
				Program    string
				Trials     int
				Minimal    bool
				Defect     generatedDefect
				Native     commandconformance.Observation
				Rejection  string
				References map[referenceuserland.ID]commandconformance.Snapshot
			}{p.Seed, p.Ordinal, c.Program, result.Program.Case().Program, result.Trials, result.Minimal, defect, lastObs.streams(), lastObs.TopLevelReject, lastRefs}, "", "  ")
			testutil.FailErr(t, "encode minimized mismatch", err)
			t.Errorf("generated mismatch:\n%s", report)
		})
	}
	t.Logf("generated seeds=%v programs=%d mismatches=%d", seeds, len(programs), mismatches)
}
