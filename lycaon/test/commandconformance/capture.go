package commandconformance

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

// References runs programs in the reference userlands; [referenceuserland.Runner] is
// the one that runs the pinned images.
type References interface {
	Run(ctx context.Context, id referenceuserland.ID, cases []referenceuserland.Case) ([]referenceuserland.Result, error)
}

// Capture runs each case in every userland it compares with, one batch per userland.
func Capture(ctx context.Context, r References, cases []Case, seed GitSeed) (map[string]map[referenceuserland.ID]Snapshot, error) {
	out := map[string]map[referenceuserland.ID]Snapshot{}
	for _, id := range referenceuserland.IDs {
		var batch []Case
		for _, c := range cases {
			if slices.Contains(c.References, id) {
				batch = append(batch, c)
			}
		}
		if len(batch) == 0 {
			continue
		}
		runs := make([]referenceuserland.Case, len(batch))
		for i, c := range batch {
			runs[i] = c.Reference(id, seed)
		}
		results, err := r.Run(ctx, id, runs)
		if err != nil {
			return nil, err
		}
		for i, c := range batch {
			if out[c.ID] == nil {
				out[c.ID] = map[referenceuserland.ID]Snapshot{}
			}
			out[c.ID][id] = snapshotOf(c.Input(id, seed), results[i])
		}
	}
	return out, nil
}

func snapshotOf(input string, result referenceuserland.Result) Snapshot {
	snap := Snapshot{Input: input, Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr}
	if len(result.Files) > 0 {
		snap.Files = map[string]*File{}
		for name, entry := range result.Files {
			snap.Files[name] = FileOf(entry)
		}
	}
	return snap
}

// Reproduces reports each stored snapshot that a new capture does not repeat byte for byte.
func Reproduces(stored, captured map[string]map[referenceuserland.ID]Snapshot) []string {
	var problems []string
	for _, caseID := range slices.Sorted(maps.Keys(captured)) {
		for _, id := range slices.Sorted(maps.Keys(captured[caseID])) {
			want, ok := stored[caseID][id]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s in %s: no stored snapshot", caseID, id))
				continue
			}
			if diff := snapshotDiff(want, captured[caseID][id]); diff.Kind != "" {
				problems = append(problems, fmt.Sprintf("%s in %s: %s", caseID, id, diff.Text))
			}
		}
	}
	return problems
}

func snapshotDiff(want, got Snapshot) Difference {
	switch {
	case want.Input != got.Input:
		return Difference{Kind: "input", Text: fmt.Sprintf("input %s, captured from %s", want.Input, got.Input)}
	case want.Status != got.Status:
		return Difference{Kind: "status", ExpectedStatus: want.Status, ActualStatus: got.Status, Text: fmt.Sprintf("status %d, now %d", want.Status, got.Status)}
	case string(want.Stdout) != string(got.Stdout):
		return Difference{Kind: "stdout", Text: fmt.Sprintf("stdout %q, now %q", want.Stdout, got.Stdout)}
	case string(want.Stderr) != string(got.Stderr):
		return Difference{Kind: "stderr", Text: fmt.Sprintf("stderr %q, now %q", want.Stderr, got.Stderr)}
	}
	return filesDiff(want.Files, got.Files)
}
