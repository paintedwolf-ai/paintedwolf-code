package conformance_test

import (
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

type generatedDefect struct {
	Kind       string
	Code       string
	References map[referenceuserland.ID]commandconformance.Difference
}

func generatedMismatch(obs runtimeObservation, refs map[referenceuserland.ID]commandconformance.Snapshot) generatedDefect {
	switch {
	case obs.TopLevelReject != "":
		return generatedDefect{Kind: "top_level_rejection"}
	case obs.Outcome.Refusal != nil:
		return generatedDefect{Kind: "refusal", Code: obs.Outcome.Refusal.Code}
	case obs.Outcome.NativeGaps != 0:
		return generatedDefect{Kind: "native_gap"}
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
