package conformance_test

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

type generatedDefect struct {
	Kind       string
	Code       string
	Facts      map[string]string
	Gaps       []toolcontract.NativeGap
	References map[referenceuserland.ID]commandconformance.Difference
}

func generatedMismatch(obs runtimeObservation, refs map[referenceuserland.ID]commandconformance.Snapshot) generatedDefect {
	switch {
	case obs.TopLevelReject != "":
		return generatedDefect{Kind: "top_level_rejection"}
	case obs.Outcome.Refusal != nil:
		facts := map[string]string{}
		for _, key := range []string{"command", "word", "option", "limit_id"} {
			if value, ok := obs.Outcome.Refusal.Details[key].(string); ok {
				facts[key] = value
			}
		}
		return generatedDefect{Kind: "refusal", Code: obs.Outcome.Refusal.Code, Facts: facts}
	case obs.Outcome.NativeGaps != 0:
		gaps := slices.Clone(obs.NativeGaps)
		slices.SortFunc(gaps, func(a, b toolcontract.NativeGap) int {
			return cmp.Or(cmp.Compare(a.Command, b.Command), cmp.Compare(a.Code, b.Code), cmp.Compare(a.Form, b.Form))
		})
		return generatedDefect{Kind: "native_gap", Gaps: slices.Compact(gaps)}
	case obs.Outcome.State != "completed":
		return generatedDefect{Kind: "program_state", Code: obs.Outcome.State}
	}
	matched, diffs := commandconformance.Match(obs.streams(), refs)
	if matched != "" {
		return generatedDefect{}
	}
	identity := maps.Clone(diffs)
	for id, diff := range identity {
		identity[id] = diff.Identity()
	}
	return generatedDefect{Kind: "observation", References: identity}
}

func generatedSelection() ([]uint64, int, error) {
	seeds := commandconformance.GeneratedSeeds()
	if raw := os.Getenv("PW_CONFORMANCE_SEEDS"); raw != "" {
		seeds = nil
		for _, word := range strings.Split(raw, ",") {
			seed, err := strconv.ParseUint(word, 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("invalid generated seed %q: %w", word, err)
			}
			seeds = append(seeds, seed)
		}
	}
	if len(seeds) > commandconformance.MaxGeneratedSeeds {
		return nil, 0, fmt.Errorf("at most %d seeds are allowed", commandconformance.MaxGeneratedSeeds)
	}
	count := commandconformance.GeneratedProgramsPerSeed
	if raw := os.Getenv("PW_CONFORMANCE_PROGRAMS"); raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid generated program count: %w", err)
		}
	}
	if count < 1 || count > commandconformance.MaxGeneratedPrograms {
		return nil, 0, fmt.Errorf("program count must be between 1 and %d", commandconformance.MaxGeneratedPrograms)
	}
	return seeds, count, nil
}
