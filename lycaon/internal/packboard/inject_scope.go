package packboard

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type InjectScope int

const (
	// InjectScopeFull includes repo, git, and worker lines.
	InjectScopeFull InjectScope = iota
	// InjectScopePulse includes only Now and worker state.
	InjectScopePulse
)

// PackBoardPulseSentinel marks a delta inject block.
const PackBoardPulseSentinel = "<!-- pack-board:pulse -->"

// BuildInjectLines assembles orientation lines for the given inject scope.
func BuildInjectLines(snapshot api.BoardSnapshot, scope InjectScope, opts OrientOpts) []string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tasks := WorkerTasksFromSnapshot(snapshot)

	var lines []string
	lines = append(lines, FormatNowLine(now))
	if host := FormatHostLine(snapshot.Host); host != "" {
		lines = append(lines, host)
	}
	if toolchains := FormatToolchainsLine(snapshot.Host); toolchains != "" {
		lines = append(lines, toolchains)
	}
	if scope == InjectScopeFull {
		if len(snapshot.OrientationRoots) < 2 {
			lines = append(lines, RepoOrientationLines(snapshot.Repo)...)
		}
		if git := FormatGitLine(snapshot.Git, snapshot.Repo); git != "" {
			lines = append(lines, git)
		}
		if wt := FormatWorktreeLine(snapshot.Git); wt != "" {
			lines = append(lines, wt)
		}
		if other := FormatOtherReposLine(snapshot.Git); other != "" {
			lines = append(lines, other)
		}
	}
	lines = append(lines, workerStateLinesWithDetail(snapshot, now, opts.IncludeDetail)...)
	if scanLines := FormatScansLines(snapshot.Scans, gitHeadShort(snapshot.Git), now, false); len(scanLines) > 0 {
		lines = append(lines, scanLines...)
	}
	if flags := FormatFlagsLine(StandingFlagsFromSnapshot(snapshot)); flags != "" {
		lines = append(lines, flags)
	}
	if opts.IncludeDetail {
		if len(NamedWorkersFromSnapshot(snapshot)) == 0 {
			for _, t := range tasks {
				lines = append(lines, FormatWorkerDetailLine(t, now))
			}
		}
	}
	return lines
}

// FormatInjectBodyScoped returns orientation lines truncated to max for host inject scope.
func FormatInjectBodyScoped(snapshot api.BoardSnapshot, scope InjectScope, omitDelegation bool, now time.Time, max int) (lines []string, didTruncate bool) {
	if max <= 0 {
		max = api.MaxBoardInjectChars
	}
	lines = BuildInjectLines(snapshot, scope, OrientOpts{OmitDelegation: omitDelegation, Now: now})
	var truncated []string
	lines, didTruncate = FitOrientationBudget(lines, max, &truncated)
	return lines, didTruncate || len(truncated) > 0
}

// InjectSentinel returns the sentinel for a host inject scope.
func InjectSentinel(scope InjectScope) string {
	if scope == InjectScopePulse {
		return PackBoardPulseSentinel
	}
	return PackBoardSentinel
}
