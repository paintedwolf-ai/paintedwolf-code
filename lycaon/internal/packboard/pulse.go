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
		strings.Join(repo.Languages, "+"),
		fmt.Sprintf("%d", repo.FileCount),
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

// PulseFingerprint hashes fast-changing board facts (clock day, workers, delegation, scans).
func PulseFingerprint(snap api.BoardSnapshot, now time.Time) string {
	return strings.Join([]string{
		OrientationDayKey(now),
		WorkerPulseSig(WorkerTasksFromSnapshot(snap), now),
		PromotePathPulseSig(PromotePathsFromSnapshot(snap)),
		DelegationPulseSig(DelegationsFromSnapshot(snap)),
		ScanPulseSig(snap.Scans),
		StandingFlagsPulseSig(StandingFlagsFromSnapshot(snap)),
	}, "|")
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

// WorkerPulseSig hashes non-terminal worker tasks for inject dedup.
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

// DelegationPulseSig returns active delegation id:phase or empty.
func DelegationPulseSig(delegations []api.Delegation) string {
	for _, d := range delegations {
		if d.Phase == api.DelegationPhaseDone {
			continue
		}
		id := d.ID
		if len(id) > 8 {
			id = id[:8]
		}
		phase := string(d.Phase)
		if phase == "" {
			phase = "active"
		}
		return id + ":" + phase
	}
	return ""
}
