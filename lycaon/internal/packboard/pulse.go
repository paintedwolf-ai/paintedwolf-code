package packboard

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

// OrientationFingerprint hashes slow-changing board facts (repo, git).
func OrientationFingerprint(snap api.BoardSnapshot) string {
	repo := snap.Repo
	hostOS := ""
	hostArch := ""
	hostExec := ""
	hostShell := ""
	if snap.Host != nil {
		hostOS = snap.Host.OS
		hostArch = snap.Host.Arch
		hostExec = string(snap.Host.ExecutionTarget)
		hostShell = fmt.Sprintf("%v", snap.Host.Shell)
	}
	gitAvailable := ""
	branch := ""
	dirty := false
	head := ""
	othersSig := ""
	if snap.Git != nil {
		gitAvailable = fmt.Sprintf("%v", snap.Git.Available)
		branch = snap.Git.Branch
		dirty = snap.Git.Dirty
		head = snap.Git.HeadShort
		if len(snap.Git.Others) > 0 || snap.Git.OthersTruncated > 0 {
			parts := make([]string, 0, len(snap.Git.Others)+1)
			for _, o := range snap.Git.Others {
				parts = append(parts, fmt.Sprintf("%s:%v:%d:%d", o.RepoID, o.Dirty, o.StagedCount, o.UnstagedCount))
			}
			if snap.Git.OthersTruncated > 0 {
				parts = append(parts, fmt.Sprintf("trunc:%d", snap.Git.OthersTruncated))
			}
			othersSig = strings.Join(parts, ",")
		}
	}
	parts := []string{
		hostOS,
		hostArch,
		hostExec,
		hostShell,
		orientationRootsSig(snap.OrientationRoots),
		FormatRepoLine(repo),
		FormatLayoutLine(repo),
		gitAvailable,
		branch,
		fmt.Sprintf("%v", dirty),
		head,
	}
	if othersSig != "" {
		parts = append(parts, othersSig)
	}
	return strings.Join(parts, "|")
}

func orientationRootsSig(roots []api.BoardOrientationRoot) string {
	if len(roots) <= 1 {
		return ""
	}
	parts := make([]string, 0, len(roots))
	for _, r := range roots {
		parts = append(parts, fmt.Sprintf("%s:%t:%d:%s:%t",
			r.Label, r.IsPrimary, r.Brief.FileCount, FormatLayoutLine(r.Brief), r.Truncated))
	}
	return strings.Join(parts, ";")
}

// PulseFingerprint hashes fast-changing board facts (clock day, workers, scans).
func PulseFingerprint(snap api.BoardSnapshot, now time.Time) string {
	return strings.Join([]string{
		OrientationDayKey(now),
		WorkerPulseSig(WorkerTasksFromSnapshot(snap), now),
		NamedWorkerPulseSig(NamedWorkersFromSnapshot(snap)),
		PromotePathPulseSig(PromotePathsFromSnapshot(snap)),
		ScanPulseSig(snap.Scans),
		StandingFlagsPulseSig(StandingFlagsFromSnapshot(snap)),
	}, "|")
}

// NamedWorkerPulseSig hashes named worker rows for inject dedup.
func NamedWorkerPulseSig(workers []api.BoardWorker) string {
	if len(workers) == 0 {
		return ""
	}
	shown := workers
	if len(shown) > MaxOrientationWorkers {
		shown = shown[:MaxOrientationWorkers]
	}
	parts := make([]string, 0, len(shown)+1)
	for _, w := range shown {
		parts = append(parts, fmt.Sprintf("%s:%s:%s:%s:%s:%s:%s",
			w.Name, w.State, w.Reason, w.WorkspaceState, w.ResultHandle, w.ResultDisposition, w.Question))
	}
	if len(workers) > MaxOrientationWorkers {
		parts = append(parts, fmt.Sprintf("omitted:%d", len(workers)-MaxOrientationWorkers))
	}
	return strings.Join(parts, ",")
}

// PromotePathPulseSig hashes cached promote path_status for inject dedup.
func PromotePathPulseSig(rows []api.WorkerPromoteJobPathStatus) string {
	if len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, job := range rows {
		pathParts := make([]string, 0, len(job.Paths))
		for _, p := range job.Paths {
			pathParts = append(pathParts, fmt.Sprintf("%s:%s:%d", p.Path, p.Status, p.HunkCount))
		}
		sort.Strings(pathParts)
		parts = append(parts, WorkerJobIDPrefix(job.WorkerID)+":"+strings.Join(pathParts, ","))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// ScanPulseSig returns latest-scan id:status:findings or empty.
func ScanPulseSig(scans *api.BoardScansSlice) string {
	if scans == nil || (scans.CurrentAssessment == nil && scans.LatestAttempt == nil) {
		return ""
	}
	s := scans.CurrentAssessment
	if scans.LatestAttempt != nil {
		s = scans.LatestAttempt
	}
	err := truncateScanBoardError(s.Error)
	sig := fmt.Sprintf("%s:%s:%d:%s", s.AssessmentID, s.Status, s.FindingsCount, err)
	if c := scans.Compare; c != nil && (c.NewCount > 0 || c.ResolvedCount > 0) {
		critical := 0
		if c.NewByLevel != nil {
			critical = c.NewByLevel[string(api.FindingLevelCritical)]
		}
		sig += fmt.Sprintf(":regression:%d:%d", critical, c.NewCount)
	}
	return sig
}

// maxScanBoardErrorRunes bounds one scan error on the orientation board.
const maxScanBoardErrorRunes = 80

func truncateScanBoardError(msg string) string {
	msg = sanitizeScanBoardError(msg)
	if msg == "" {
		return ""
	}
	// Fit, not Clamp: the board line has a fixed width and the marker counts.
	return runeclamp.Fit(msg, maxScanBoardErrorRunes)
}

func sanitizeScanBoardError(msg string) string {
	msg = strings.TrimSpace(msg)
	for _, prefix := range []string{"opengrep parse: opengrep_json: ", "opengrep_json: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// WorkerPulseSig hashes active jobs and outstanding partial identities for inject dedup.
func WorkerPulseSig(tasks []api.WorkerTask, now time.Time) string {
	type row struct {
		id, status string
		started    int64
	}
	var rows []row
	for _, t := range tasks {
		if t.Status.IsTerminal() {
			continue
		}
		var started int64
		if t.StartedAt != nil {
			started = t.StartedAt.UTC().Unix()
		}
		rows = append(rows, row{id: t.ID, status: string(t.Status), started: started})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s:%s:%d", r.id, r.status, r.started))
	}
	_ = now
	return strings.Join(parts, ",")
}
