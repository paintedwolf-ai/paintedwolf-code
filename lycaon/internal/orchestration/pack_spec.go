package orchestration

import (
	"fmt"
	"strings"
)

func effectivePackCount(spec PackSpec) int {
	if spec.Count > 0 {
		return spec.Count
	}
	return DefaultPackCount
}

func effectivePackProfile(spec PackSpec) string {
	profileID := strings.TrimSpace(spec.ProfileID)
	if profileID == "" {
		return ProfilePathExplorer
	}
	return profileID
}

func effectivePackMergeStrategy(spec PackSpec) MergeStrategy {
	if spec.MergeStrategy == "" {
		return MergeFirstValid
	}
	return spec.MergeStrategy
}

// validatePackSpec checks pack topology configuration at load and run time.
func validatePackSpec(spec PackSpec) error {
	count := effectivePackCount(spec)
	if count > MaxTeamAgents {
		return fmt.Errorf("pack count (%d) exceeds max (%d)", count, MaxTeamAgents)
	}
	switch effectivePackMergeStrategy(spec) {
	case MergeFirstValid, MergeConsensus, MergeUnion:
		return nil
	default:
		return fmt.Errorf("unknown merge_strategy %q", spec.MergeStrategy)
	}
}

func packProbeKey(index int) string {
	return fmt.Sprintf("probe-%d", index)
}
