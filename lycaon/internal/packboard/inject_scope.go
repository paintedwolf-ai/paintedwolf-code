package packboard

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type InjectScope int

const (
	// InjectScopeFull includes repo, git, work, and delegation lines (first turn or orientation change).
	InjectScopeFull InjectScope = iota
	// InjectScopePulse includes only Now, work, and delegation — stable repo/git omitted.
	InjectScopePulse
)

// PackBoardPulseSentinel marks a delta inject block (worker/delegation pulse only).
const PackBoardPulseSentinel = "<!-- pack-board:pulse -->"

// BuildInjectLines assembles orientation lines for the given inject scope.
func BuildInjectLines(snapshot api.BoardSnapshot, scope InjectScope, opts OrientOpts) []string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tasks := WorkerTasksFromSnapshot(snapshot)
	delegations := DelegationsFromSnapshot(snapshot)

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
	if work := FormatWorkLine(tasks, now); work != "" {
		lines = append(lines, work)
	}
	if scope == InjectScopePulse {
		for _, t := range inFlightWorkerTasks(tasks) {
			if line := FormatInFlightWorkerPulseLine(t); line != "" {
				lines = append(lines, line)
			}
		}
	}
	if scanLines := FormatScansLines(snapshot.Scans, gitHeadShort(snapshot.Git), now, false); len(scanLines) > 0 {
		lines = append(lines, scanLines...)
	}
	if flags := FormatFlagsLine(StandingFlagsFromSnapshot(snapshot)); flags != "" {
		lines = append(lines, flags)
	}
	if !opts.OmitDelegation && !opts.IncludeDetail {
		if line := FormatDelegationLine(delegations); line != "" {
			lines = append(lines, line)
		}
	}
	if opts.IncludeDetail {
		for _, t := range tasks {
			lines = append(lines, FormatWorkerDetailLine(t, now))
		}
		for _, d := range delegations {
			if line := FormatDelegationDetailLine(d); line != "" {
				lines = append(lines, line)
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
