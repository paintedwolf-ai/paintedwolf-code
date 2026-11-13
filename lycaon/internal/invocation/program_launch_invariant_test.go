package invocation

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// Whatever a & job runs, its work folds into the launch's one activity and
// nowhere else, the launch's state follows its most significant step, and
// foreground work beside the job keeps its own rows.
func TestBackgroundJobWorkAlwaysFoldsIntoItsLaunch(t *testing.T) {
	kinds := []func(id, parent string) activityInput{
		func(id, parent string) activityInput {
			in := activityFixture(id, parent, "command", "external", 0)
			in.arguments["command"] = "server " + id
			return in
		},
		func(id, parent string) activityInput {
			return withReads(activityFixture(id, parent, "sed", "sed", 0), id+".txt")
		},
		func(id, parent string) activityInput {
			in := activityFixture(id, parent, "write", "write", 0)
			in.receipt.EffectClasses = []string{"mutation"}
			in.arguments["path_targets"] = map[string]any{"read": []any{}, "write": []any{id + ".log"}}
			return in
		},
		func(id, parent string) activityInput { return activityFixture(id, parent, "wait", "sleep", 0) },
	}
	counts := []activityCounts{{total: 1}, {total: 1, running: 1}, {total: 1, failed: 1}, {total: 2, waiting: 1}}
	random := rand.New(rand.NewPCG(3, 4))
	for trial := range 500 {
		launch := activityFixture("job", "root", "program &", "", 0)
		launch.receipt.JobHandle = "registry-handle"
		groups := []activityGroup{{input: launch, key: activityKey{launch: "job"}, parent: activityKey{node: "root"}, counts: activityCounts{total: 1}}}
		parents := []activityKey{{launch: "job"}}
		var descendants []string
		for i := range 1 + random.IntN(6) {
			parent := parents[random.IntN(len(parents))]
			id := fmt.Sprintf("step-%d", i)
			if random.IntN(4) == 0 {
				pipe := activityFixture(id, "job", "program |", "", 0)
				groups = append(groups, activityGroup{input: pipe, key: activityKey{node: id}, parent: parent, counts: activityCounts{total: 1}})
				parents = append(parents, activityKey{node: id})
				continue
			}
			input := kinds[random.IntN(len(kinds))](id, "job")
			groups = append(groups, activityGroup{input: input, key: activityKey{node: id}, parent: parent, counts: counts[random.IntN(len(counts))]})
			descendants = append(descendants, id)
		}
		foreground := activityFixture("fetch", "root", "http_request", "curl", 0)
		foreground.arguments["url"] = "http://127.0.0.1:8000"
		groups = append(groups, activityGroup{input: foreground, key: activityKey{node: "fetch"}, parent: activityKey{node: "root"}, counts: activityCounts{total: 1}})
		for i := range groups {
			groups[i].ordinal = int64(i + 1)
		}

		values := composeActivityGroups(groups, random.IntN(2) == 0)
		var job []int
		owner := map[string]int{}
		for row, activity := range values {
			if activity.JobHandle == launch.receipt.JobHandle {
				job = append(job, row)
			}
			for _, id := range activity.ExecutionIDs {
				if previous, seen := owner[id]; seen {
					t.Fatalf("trial %d: execution %s in rows %d and %d: %+v", trial, id, previous, row, values)
				}
				owner[id] = row
			}
		}
		if len(job) != 1 || values[job[0]].ID != "job" {
			t.Fatalf("trial %d: launch rows %v: %+v", trial, job, values)
		}
		want := activityStepState(groups[0].counts, launch.receipt)
		for _, id := range descendants {
			if row, ok := owner[id]; !ok || row != job[0] {
				t.Fatalf("trial %d: job step %s outside its launch: %+v", trial, id, values)
			}
		}
		for _, g := range groups[1 : len(groups)-1] {
			if activityPresentation(g.input).role != "container" {
				if state := activityStepState(g.counts, g.input.receipt); activityStateRank[state] > activityStateRank[want] {
					want = state
				}
			}
		}
		if got := values[job[0]].State; got != want {
			t.Fatalf("trial %d: launch state %q, want %q", trial, got, want)
		}
		if row, ok := owner["fetch"]; !ok || row == job[0] {
			t.Fatalf("trial %d: foreground work folded into the job: %+v", trial, values)
		}
		if !slices.Contains(values[job[0]].ExecutionIDs, "job") {
			t.Fatalf("trial %d: launch lost its own execution", trial)
		}
	}
}
