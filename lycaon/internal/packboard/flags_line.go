package packboard

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/standingpatterns"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	standingFlagsSnapshotKey = "standing_flags"
	flagsLinePrefix          = "Flags: "
)

// FormatFlagsLine renders standing anti-pattern counts for board orientation.
// Example: Flags: panic ×3 · console.log ×1
func FormatFlagsLine(flags []standingpatterns.FlagCount) string {
	if len(flags) == 0 {
		return ""
	}
	var parts []string
	for _, f := range flags {
		if f.Count <= 0 {
			continue
		}
		suffix := ""
		if f.Truncated {
			suffix = "+"
		}
		parts = append(parts, fmt.Sprintf("%s ×%d%s", f.Label, f.Count, suffix))
	}
	if len(parts) == 0 {
		return ""
	}
	return renderFlagsLine(parts, MaxFlagsLineBytes)
}

func renderFlagsLine(parts []string, budget int) string {
	if budget <= len(flagsLinePrefix) {
		return ""
	}
	remaining := budget - len(flagsLinePrefix)
	var shown []string
	for _, part := range parts {
		candidate := part
		if len(shown) > 0 {
			candidate = strings.Join(append(shown, part), " · ")
		}
		if len(candidate) > remaining {
			break
		}
		shown = append(shown, part)
	}
	if len(shown) == 0 {
		return ""
	}
	line := flagsLinePrefix + strings.Join(shown, " · ")
	rest := len(parts) - len(shown)
	if rest > 0 {
		suffix := fmt.Sprintf(" +%d more", rest)
		if len(line)+len(suffix) <= budget {
			line += suffix
		}
	}
	return line
}

// StandingFlagsFromSnapshot returns cached standing-pattern counts from a board snapshot.
func StandingFlagsFromSnapshot(snap api.BoardSnapshot) []standingpatterns.FlagCount {
	if snap.Workers == nil {
		return nil
	}
	raw, _ := (*snap.Workers)[standingFlagsSnapshotKey].([]standingpatterns.FlagCount)
	return raw
}

// EnrichSnapshotStandingFlags attaches standing-pattern match counts to a board snapshot.
func EnrichSnapshotStandingFlags(snap *api.BoardSnapshot, flags []standingpatterns.FlagCount) {
	if snap == nil || len(flags) == 0 {
		return
	}
	if snap.Workers == nil {
		snap.Workers = &api.BoardWorkersSlice{}
	}
	(*snap.Workers)[standingFlagsSnapshotKey] = flags
}

// StandingFlagsPulseSig hashes standing-pattern counts for inject dedup.
func StandingFlagsPulseSig(flags []standingpatterns.FlagCount) string {
	if len(flags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(flags))
	for _, f := range flags {
		trunc := ""
		if f.Truncated {
			trunc = "+"
		}
		parts = append(parts, fmt.Sprintf("%s:%d%s", f.Label, f.Count, trunc))
	}
	return strings.Join(parts, ";")
}

func isFlagsLine(line string) bool {
	return strings.HasPrefix(line, flagsLinePrefix)
}

func dropFlagsLine(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if isFlagsLine(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}
