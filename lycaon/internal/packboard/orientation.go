package packboard

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

// shortIDBytes bounds an id rendered inline on the board.
const shortIDBytes = 4

// PackBoardSentinel marks compact pack-board inject/tool output blocks.
const PackBoardSentinel = "<!-- pack-board:v1 -->"

// OrientOpts controls orientation line assembly.
type OrientOpts struct {
	OmitDelegation    bool
	Now               time.Time
	IncludeDetail     bool
	TruncatedSections *[]string
}

// FormatNowLine returns a local clock line: "Now: YYYY-MM-DD HH:MM TZ".
func FormatNowLine(now time.Time) string {
	local := now.Local()
	tz := local.Format("MST")
	if strings.HasPrefix(tz, "+") || strings.HasPrefix(tz, "-") {
		_, offset := local.Zone()
		tz = formatTZAbbrev(offset)
	}
	return "Now: " + local.Format("2006-01-02 15:04") + " " + tz
}

// OrientationDayKey returns local YYYY-MM-DD for pack_content_hash day boundary.
func OrientationDayKey(now time.Time) string {
	return now.Local().Format("2006-01-02")
}

// BuildOrientationLines assembles pack board orientation lines (no sentinel, no truncation).
func BuildOrientationLines(snapshot api.BoardSnapshot, opts OrientOpts) []string {
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
	lines = append(lines, RepoOrientationLines(snapshot.Repo)...)
	if git := FormatGitLine(snapshot.Git, snapshot.Repo); git != "" {
		lines = append(lines, git)
	}
	if wt := FormatWorktreeLine(snapshot.Git); wt != "" {
		lines = append(lines, wt)
	}
	if other := FormatOtherReposLine(snapshot.Git); other != "" {
		lines = append(lines, other)
	}
	if work := FormatWorkLine(tasks, now); work != "" {
		lines = append(lines, work)
	}
	if merge := FormatBranchMergeLine(tasks); merge != "" {
		lines = append(lines, merge)
	}
	if planLine := FormatOverlayMergePlanLine(OverlayMergePlanFromSnapshot(snapshot)); planLine != "" {
		lines = append(lines, planLine)
	}
	if pathLine := FormatPromotePathStatusLine(PromotePathsFromSnapshot(snapshot)); pathLine != "" {
		lines = append(lines, pathLine)
	}
	if scanLines := FormatScansLines(snapshot.Scans, gitHeadShort(snapshot.Git), now, opts.IncludeDetail); len(scanLines) > 0 {
		lines = append(lines, scanLines...)
	}
	if flags := FormatFlagsLine(StandingFlagsFromSnapshot(snapshot)); flags != "" {
		lines = append(lines, flags)
	}
	if reserved := FormatReservedPathsLines(ReservationsFromSnapshot(snapshot)); len(reserved) > 0 {
		lines = append(lines, reserved...)
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

// TruncateLinesBottomUp drops trailing lines until the joined body fits max.
func TruncateLinesBottomUp(lines []string, max int, truncated *[]string) ([]string, bool) {
	for len(lines) > 0 {
		body := strings.Join(lines, "\n")
		if len(body) <= max {
			return lines, false
		}
		dropped := lines[len(lines)-1]
		lines = lines[:len(lines)-1]
		if truncated != nil {
			*truncated = append(*truncated, dropped)
		}
	}
	return lines, true
}

// FormatOverlayMergePlanLine summarizes pending overlay promote order for board orientation.
func FormatOverlayMergePlanLine(plan *api.OverlayMergePlan) string {
	if plan == nil || plan.PendingCount == 0 {
		return ""
	}
	line := fmt.Sprintf("Overlay plan: %d pending", plan.PendingCount)
	if len(plan.PromoteSequence) > 0 {
		parts := make([]string, 0, len(plan.PromoteSequence))
		for _, id := range plan.PromoteSequence {
			parts = append(parts, WorkerJobIDPrefix(id))
		}
		line += " · promote " + strings.Join(parts, " → ")
	}
	if len(plan.SharedPaths) > 0 {
		var sharedParts []string
		for _, row := range plan.SharedPaths {
			if len(row.WorkerIDs) < 2 {
				continue
			}
			chips := make([]string, 0, len(row.WorkerIDs))
			for _, id := range row.WorkerIDs {
				chips = append(chips, WorkerJobIDPrefix(id))
			}
			sharedParts = append(sharedParts, fmt.Sprintf("%s (%s)", row.Path, strings.Join(chips, ",")))
		}
		if len(sharedParts) > 0 {
			line += " · shared " + strings.Join(sharedParts, "; ")
		}
	}
	return line
}

// FormatPromotePathStatusLine summarizes cached path_status from promote_overlay / preview_overlay.
func FormatPromotePathStatusLine(rows []api.WorkerPromoteJobPathStatus) string {
	if len(rows) == 0 {
		return ""
	}
	var parts []string
	for _, job := range rows {
		if len(job.Paths) == 0 {
			continue
		}
		pathParts := make([]string, 0, len(job.Paths))
		for _, p := range job.Paths {
			path := strings.TrimSpace(p.Path)
			if path == "" {
				continue
			}
			status := string(p.Status)
			if p.PromoteOrder != "" && p.PromoteOrder != api.WorkerPromoteOrderIndependent {
				status = status + ":" + string(p.PromoteOrder)
			}
			if p.Status == api.WorkerPromotePathOutcomeConflict && p.HunkCount > 0 {
				pathParts = append(pathParts, fmt.Sprintf("%s:%s(%d)", path, status, p.HunkCount))
			} else {
				pathParts = append(pathParts, fmt.Sprintf("%s:%s", path, status))
			}
		}
		if len(pathParts) == 0 {
			continue
		}
		parts = append(parts, WorkerJobIDPrefix(job.WorkerID)+" "+strings.Join(pathParts, " "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Merge paths: " + strings.Join(parts, " · ")
}

func FormatWorkLine(tasks []api.WorkerTask, now time.Time) string {
	inFlight := inFlightWorkerTasks(tasks)
	if len(inFlight) > 0 {
		line := fmt.Sprintf("Work: %d in flight", len(inFlight))
		chips := make([]string, 0, len(inFlight))
		for _, t := range inFlight {
			if chip := formatInFlightWorkerChip(t); chip != "" {
				chips = append(chips, chip)
			}
		}
		if len(chips) > 0 {
			line += " · " + strings.Join(chips, " · ")
		}
		oldest := oldestStartedAt(inFlight)
		if oldest != nil {
			if rel := FormatRelativeSince(now, *oldest); rel != "" {
				line += " · " + rel
			}
		}
		return line
	}
	var done []api.WorkerTask
	for _, t := range tasks {
		if t.Status.IsTerminal() {
			done = append(done, t)
		}
	}
	if len(done) == 0 {
		return ""
	}
	line := fmt.Sprintf("Work: %d done", len(done))
	last := latestCompletedAt(done)
	if last != nil {
		if rel := FormatRelativeDuration(now, *last); rel != "" {
			line += " · last " + rel
		}
	}
	return line
}

// FormatBranchMergeLine lists write-branch merge_status for board orientation.
func FormatBranchMergeLine(tasks []api.WorkerTask) string {
	var pendingIDs, mergedIDs, rebasingIDs, orphanedIDs, rejectedIDs []string
	for _, t := range tasks {
		if strings.TrimSpace(t.WorkspaceRoot) == "" && t.MergeStatus == "" {
			continue
		}
		switch t.MergeStatus {
		case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying:
			pendingIDs = append(pendingIDs, WorkerJobIDPrefix(t.ID))
		case api.WorkerMergeStatusRebasing:
			rebasingIDs = append(rebasingIDs, WorkerJobIDPrefix(t.ID))
		case api.WorkerMergeStatusMerged:
			mergedIDs = append(mergedIDs, WorkerJobIDPrefix(t.ID))
		case api.WorkerMergeStatusOrphaned:
			orphanedIDs = append(orphanedIDs, WorkerJobIDPrefix(t.ID))
		case api.WorkerMergeStatusRejected:
			rejectedIDs = append(rejectedIDs, WorkerJobIDPrefix(t.ID))
		case api.WorkerMergeStatusAborted:
		}
	}
	if len(pendingIDs)+len(mergedIDs)+len(rebasingIDs)+len(orphanedIDs)+len(rejectedIDs) == 0 {
		return ""
	}
	var parts []string
	if len(pendingIDs) > 0 {
		parts = append(parts, fmt.Sprintf("pending %s", strings.Join(pendingIDs, ",")))
	}
	if len(rebasingIDs) > 0 {
		parts = append(parts, fmt.Sprintf("rebasing %s", strings.Join(rebasingIDs, ",")))
	}
	if len(mergedIDs) > 0 {
		parts = append(parts, fmt.Sprintf("merged %s", strings.Join(mergedIDs, ",")))
	}
	if len(orphanedIDs) > 0 {
		parts = append(parts, fmt.Sprintf("orphaned %s", strings.Join(orphanedIDs, ",")))
	}
	if len(rejectedIDs) > 0 {
		parts = append(parts, fmt.Sprintf("rejected %s", strings.Join(rejectedIDs, ",")))
	}
	return "Merge: " + strings.Join(parts, " · ")
}

func inFlightWorkerTasks(tasks []api.WorkerTask) []api.WorkerTask {
	var out []api.WorkerTask
	for _, t := range tasks {
		if !t.Status.IsTerminal() {
			out = append(out, t)
		}
	}
	return out
}

func formatInFlightWorkerChip(t api.WorkerTask) string {
	agent := strings.TrimSpace(t.AgentType)
	if agent == "" {
		agent = "worker"
	}
	return WorkerJobIDPrefix(t.ID) + " " + agent
}

// WorkerJobIDPrefix returns a short job id prefix for roster lines (e.g. 8a3f…).
func WorkerJobIDPrefix(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "????"
	}
	return runeclamp.ClampBytes(id, shortIDBytes)
}

// FormatInFlightWorkerPulseLine renders one pulse roster line for an in-flight job.
func FormatInFlightWorkerPulseLine(t api.WorkerTask) string {
	agent := strings.TrimSpace(t.AgentType)
	if agent == "" {
		agent = "worker"
	}
	line := fmt.Sprintf("In flight: %s · %s · %s · %s", WorkerJobIDPrefix(t.ID), agent, t.Status, t.EffectiveScope().Summary())
	if brief := strings.Join(strings.Fields(t.Brief), " "); brief != "" {
		line += " · " + runeclamp.ClampBytes(brief, 240)
	}
	if touch := formatTouchSummary(t.TouchedPaths); touch != "" {
		line += " · " + touch
	}
	line += formatWorkerBudgetSegment(t)
	return line
}

func formatTouchSummary(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return "touched · " + paths[0]
	}
	return "touched · " + paths[0] + " (+ " + strconv.Itoa(len(paths)-1) + " more)"
}

// FormatScansLines renders compact inject or full per-kind scan summaries.
func FormatScansLines(scans *api.BoardScansSlice, gitHeadShort string, now time.Time, full bool) []string {
	if scans == nil || (scans.CurrentAssessment == nil && scans.LatestAttempt == nil) {
		return nil
	}
	if full {
		return FormatScansDetailLines(scans, gitHeadShort, now)
	}
	lines := make([]string, 0, 2)
	if line := FormatScansLine(scans.CurrentAssessment, scans.Compare, gitHeadShort, now); line != "" {
		lines = append(lines, line)
	}
	if line := FormatScansLine(scans.LatestAttempt, nil, gitHeadShort, now); line != "" {
		lines = append(lines, strings.Replace(line, "Scan:", "Latest scan attempt:", 1))
	}
	return lines
}

// FormatScansLine renders a one-line summary of the most recent scan.
func FormatScansLine(summary *api.BoardScanSummary, compare *api.BoardScanCompareSlice, gitHeadShort string, now time.Time) string {
	if summary == nil {
		return ""
	}
	s := summary
	parts := []string{"Scan: " + string(s.Status)}
	if id := shortScanBoardID(s.AssessmentID); id != "" {
		parts = append(parts, "assessment "+id)
	}
	if head := strings.TrimSpace(s.HeadShort); head != "" {
		parts = append(parts, "head "+head)
	}
	if cats := scanCategoriesLabel(s.Categories); cats != "" {
		parts = append(parts, cats)
	}
	switch s.Status {
	case api.CodeScanStatusComplete:
		if s.CoverageStatus != "" && s.CoverageStatus != api.ScanCoverageComplete {
			parts = append(parts, "coverage "+string(s.CoverageStatus))
		}
		if s.FindingsCount == 0 && len(s.WarningSummary) == 0 && (s.CoverageStatus == "" || s.CoverageStatus == api.ScanCoverageComplete) {
			parts = append(parts, "clean")
		} else {
			parts = append(parts, fmt.Sprintf("%d findings", s.FindingsCount))
			if tail := formatCompareRegressionTail(compare); tail != "" {
				parts = append(parts, tail)
			}
			if compare != nil {
				if baseline := shortScanBoardID(compare.BaselineAssessmentID); baseline != "" {
					parts = append(parts, "prior assessment "+baseline)
				}
			}
			if tail := formatFindingsLevelTail(s.FindingsByLevel); tail != "" {
				parts = append(parts, tail)
			}
			if top := formatTopLocationsSegment(s.TopLocations, boardTopLocationsInjectCap()); top != "" {
				parts = append(parts, top)
			}
		}
		if warn := formatScanWarningsBoardSegment(s.WarningSummary); warn != "" {
			parts = append(parts, "warn: "+warn)
		}
	case api.CodeScanStatusFailed:
		parts = append(parts, "not clean")
		if msg := truncateScanBoardError(s.Error); msg != "" {
			parts = append(parts, "err: "+msg)
		}
	case api.CodeScanStatusTimedOut:
		parts = append(parts, "timed out")
		if msg := truncateScanBoardError(s.Error); msg != "" {
			parts = append(parts, "err: "+msg)
		}
	case api.CodeScanStatusSuperseded:
		parts = append(parts, "superseded")
	case api.CodeScanStatusCanceled:
		parts = append(parts, "canceled")
	case api.CodeScanStatusPending:
		parts = append(parts, "queued")
	case api.CodeScanStatusRunning:
		if s.LongRunning {
			parts = append(parts, "long-running")
		}
	}
	if gitHeadShort != "" && s.HeadShort != "" && s.HeadShort != gitHeadShort {
		parts = append(parts, "stale vs git")
	}
	when := s.CompletedAt
	if when == nil || when.IsZero() {
		when = s.StartedAt
		if when == nil || when.IsZero() {
			when = &s.CreatedAt
		}
	}
	if !when.IsZero() {
		if s.Status == api.CodeScanStatusRunning || s.Status == api.CodeScanStatusPending {
			if rel := FormatRelativeSince(now, *when); rel != "" {
				parts = append(parts, rel)
			}
		} else {
			if rel := FormatRelativeDuration(now, *when); rel != "" {
				parts = append(parts, rel)
			}
		}
	}
	return strings.Join(parts, " · ")
}

func formatCompareRegressionTail(compare *api.BoardScanCompareSlice) string {
	if compare == nil {
		return ""
	}
	if compare.NewCount > 0 {
		critical := 0
		if compare.NewByLevel != nil {
			critical = compare.NewByLevel[string(api.FindingLevelCritical)]
		}
		if critical > 0 {
			return fmt.Sprintf("regression · +%d critical", critical)
		}
		return fmt.Sprintf("+%d new", compare.NewCount)
	}
	if compare.ResolvedCount > 0 {
		return fmt.Sprintf("−%d resolved", compare.ResolvedCount)
	}
	return ""
}

// FormatScansDetailLines renders multi-line per-kind scan rows for detail_level full.
func FormatScansDetailLines(scans *api.BoardScansSlice, gitHeadShort string, now time.Time) []string {
	if scans == nil || (scans.CurrentAssessment == nil && scans.LatestAttempt == nil) {
		return nil
	}
	if scans.CurrentAssessment == nil {
		line := FormatScansLine(scans.LatestAttempt, nil, gitHeadShort, now)
		if line == "" {
			return nil
		}
		return []string{strings.Replace(line, "Scan:", "Latest scan attempt:", 1)}
	}
	summary := scans.CurrentAssessment
	line := FormatScansLine(summary, scans.Compare, gitHeadShort, now)
	if line == "" {
		return nil
	}
	if summary == nil || summary.Status != api.CodeScanStatusComplete {
		return []string{line}
	}
	byKind := summary.FindingsByKind
	if len(byKind) == 0 {
		return []string{line}
	}
	kinds := []struct {
		label string
		key   string
	}{
		{"SAST", string(api.FindingKindSAST)},
		{"SCA", string(api.FindingKindSCA)},
		{"Secret", string(api.FindingKindSecret)},
	}
	lines := []string{line}
	if attempt := FormatScansLine(scans.LatestAttempt, nil, gitHeadShort, now); attempt != "" {
		lines = append(lines, strings.Replace(attempt, "Scan:", "Latest scan attempt:", 1))
	}
	for i, kind := range kinds {
		count := byKind[kind.key]
		prefix := "├─"
		if i == len(kinds)-1 {
			prefix = "└─"
		}
		segment := fmt.Sprintf("%s %s %d", prefix, kind.label, count)
		if kind.key == string(api.FindingKindSAST) && summary.UnmappedCount > 0 {
			segment += fmt.Sprintf(" · %d unmapped", summary.UnmappedCount)
		}
		if top := formatTopLocationsSegment(summary.TopLocations, 5); top != "" && count > 0 {
			segment += " · " + top
		}
		lines = append(lines, segment)
	}
	if compare := scans.Compare; compare != nil {
		for _, stub := range compare.TopNew {
			if stub.File == "" || stub.RuleID == "" {
				continue
			}
			lines = append(lines, "  "+stub.File+":"+stub.RuleID)
		}
	}
	return lines
}

func gitHeadShort(git *api.BoardGitSlice) string {
	if git == nil {
		return ""
	}
	return strings.TrimSpace(git.HeadShort)
}

func boardTopLocationsInjectCap() int {
	return 3
}

func formatTopLocationsSegment(locs []api.BoardScanTopLocation, limit int) string {
	if limit <= 0 || len(locs) == 0 {
		return ""
	}
	if len(locs) > limit {
		locs = locs[:limit]
	}
	parts := make([]string, 0, len(locs))
	for _, loc := range locs {
		uri := strings.TrimSpace(loc.URI)
		if uri == "" {
			continue
		}
		if loc.StartLine > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", uri, loc.StartLine))
		} else {
			parts = append(parts, uri)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "top: " + strings.Join(parts, ", ")
}

func formatFindingsLevelTail(byLevel map[string]int) string {
	if len(byLevel) == 0 {
		return ""
	}
	errCount := byLevel[string(api.FindingLevelCritical)] + byLevel[string(api.FindingLevelHigh)]
	warnCount := byLevel[string(api.FindingLevelMedium)]
	parts := make([]string, 0, 2)
	if errCount > 0 {
		parts = append(parts, fmt.Sprintf("%derr", errCount))
	}
	if warnCount > 0 {
		parts = append(parts, fmt.Sprintf("%dwarn", warnCount))
	}
	return strings.Join(parts, " ")
}

func formatScanWarningsBoardSegment(summaries []api.ScanWarningSummary) string {
	count := 0
	for _, summary := range summaries {
		count += summary.Count
	}
	if count == 0 {
		return ""
	}
	if count == 1 {
		return "1 analysis limitation; inspect scan_summary"
	}
	return fmt.Sprintf("%d analysis limitations; inspect scan_summary", count)
}

func shortScanBoardID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func scanCategoriesLabel(cats []api.ScanCategory) string {
	if len(cats) == 0 {
		return ""
	}
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		s := strings.TrimSpace(string(c))
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

// FormatDelegationLine summarizes the first unsettled delegation; detail
// output lists every one through FormatDelegationDetailLine instead.
func FormatDelegationLine(delegations []api.Delegation) string {
	for _, d := range delegations {
		if d.Phase == api.DelegationPhaseDone {
			continue
		}
		phase := string(d.Phase)
		if phase == "" {
			phase = "active"
		}
		id := d.ID
		if len(id) > 8 {
			id = id[:8]
		}
		return "Delegation: " + id + " · " + phase
	}
	return ""
}

// Unknown workspaces retain the file-survey suggestion.
const GitNoneOrientationLine = "Git: none · verify with read/grep"

// Measured empty workspaces omit the file-survey suggestion.
const GitNoneEmptyRepoOrientationLine = "Git: none"

// RepoEmptyOrientationLine applies only to measured empty workspaces.
const RepoEmptyOrientationLine = "Repo: empty · no files yet · skip read scouts"

// RepoKnownEmpty requires a settled, exhaustive measurement.
func RepoKnownEmpty(repo api.RepoBrief) bool {
	return !repo.Refreshing && !repo.Incomplete && !repo.GeneratedAt.IsZero() && repo.FileCount == 0 && len(repo.Languages) == 0
}

// RepoOrientationLines renders repo + layout orientation lines for a brief.
func RepoOrientationLines(repo api.RepoBrief) []string {
	repoLine := FormatRepoLine(repo)
	if repoLine == "" {
		return nil
	}
	out := []string{repoLine}
	if layout := FormatLayoutLine(repo); layout != "" {
		out = append(out, layout)
	}
	return out
}

// FormatRepoLine distinguishes measured emptiness from incomplete inventory.
func FormatRepoLine(repo api.RepoBrief) string {
	if repo.FileCount == 0 && len(repo.Languages) == 0 {
		if RepoKnownEmpty(repo) {
			return RepoEmptyOrientationLine
		}
		if repo.Incomplete {
			return "Repo: file inventory incomplete"
		}
		return ""
	}
	line := fmt.Sprintf("Repo: %d files", repo.FileCount)
	if repo.Incomplete {
		line = fmt.Sprintf("Repo: %d observed files", repo.FileCount)
	}
	if len(repo.Languages) > 0 {
		line += " · " + strings.Join(repo.Languages, "+")
	}
	if repo.Refreshing {
		line += " · refreshing"
	} else if repo.Incomplete {
		line += " · incomplete coverage"
	}
	return line
}

func FormatGitLine(git *api.BoardGitSlice, repo api.RepoBrief) string {
	if git == nil {
		return ""
	}
	if !git.Available {
		if RepoKnownEmpty(repo) {
			return GitNoneEmptyRepoOrientationLine
		}
		return GitNoneOrientationLine
	}
	if strings.TrimSpace(git.Branch) == "" {
		return ""
	}
	if git.Dirty {
		n := git.StagedCount + git.UnstagedCount
		return fmt.Sprintf("Git: %s · dirty (%d)", git.Branch, n)
	}
	return "Git: " + git.Branch + " · clean"
}

// Unbound sessions omit the worktree line.
func FormatWorktreeLine(git *api.BoardGitSlice) string {
	if git == nil || git.Worktree == nil {
		return ""
	}
	wt := git.Worktree
	branch := strings.TrimSpace(wt.Branch)
	base := strings.TrimSpace(wt.BaseBranch)
	if branch == "" || base == "" {
		return ""
	}
	line := fmt.Sprintf("Worktree: %s from %s · %d ahead", branch, base, wt.AheadOfBase)
	if wt.BehindBase > 0 {
		line += fmt.Sprintf(" · %d behind", wt.BehindBase)
	}
	if wt.Dirty {
		line += " · uncommitted"
	}
	return line
}

// FormatOtherReposLine includes only dirty, non-active repositories.
func FormatOtherReposLine(git *api.BoardGitSlice) string {
	if git == nil || len(git.Others) == 0 {
		return ""
	}
	segs := make([]string, 0, len(git.Others))
	for _, o := range git.Others {
		if !o.Dirty {
			continue
		}
		n := o.StagedCount + o.UnstagedCount
		label := strings.TrimSpace(o.Label)
		branch := strings.TrimSpace(o.Branch)
		segs = append(segs, fmt.Sprintf("%s %s · dirty (%d)", label, branch, n))
	}
	if len(segs) == 0 {
		return ""
	}
	line := "Other repos: " + strings.Join(segs, " · ")
	if git.OthersTruncated > 0 {
		line += fmt.Sprintf(" · +%d more", git.OthersTruncated)
	}
	return line
}

func FormatWorkerDetailLine(t api.WorkerTask, now time.Time) string {
	agent := strings.TrimSpace(t.AgentType)
	if agent == "" {
		agent = "worker"
	}
	line := "Task: " + agent + " · " + string(t.Status)
	if t.StartedAt != nil {
		line += " · " + FormatRelativeSince(now, *t.StartedAt)
	}
	line += formatWorkerBudgetSegment(t)
	return line
}

func formatWorkerBudgetSegment(t api.WorkerTask) string {
	if t.MaxToolLoops <= 0 {
		return ""
	}
	used := t.ToolLoopsUsed
	if used < 0 {
		used = 0
	}
	return fmt.Sprintf(" · %d/%d", used, t.MaxToolLoops)
}

func FormatDelegationDetailLine(d api.Delegation) string {
	if d.Phase == api.DelegationPhaseDone {
		return ""
	}
	id := d.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return "Delegation: " + id + " · " + string(d.Phase) + " · " + strings.TrimSpace(d.Task)
}

func oldestStartedAt(tasks []api.WorkerTask) *time.Time {
	var best *time.Time
	for _, t := range tasks {
		if t.StartedAt == nil {
			continue
		}
		if best == nil || t.StartedAt.Before(*best) {
			ts := t.StartedAt.UTC()
			best = &ts
		}
	}
	return best
}

func latestCompletedAt(tasks []api.WorkerTask) *time.Time {
	var best *time.Time
	for _, t := range tasks {
		if t.CompletedAt == nil {
			continue
		}
		if best == nil || t.CompletedAt.After(*best) {
			ts := t.CompletedAt.UTC()
			best = &ts
		}
	}
	return best
}

func formatTZAbbrev(offsetSec int) string {
	if offsetSec == 0 {
		return "UTC"
	}
	hours := offsetSec / 3600
	if hours == 0 {
		return "UTC"
	}
	if hours > 0 {
		return "UTC+" + itoa(hours)
	}
	return "UTC" + itoa(hours)
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
