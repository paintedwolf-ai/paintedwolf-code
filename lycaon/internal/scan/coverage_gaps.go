package scan

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// A scan's coverage gaps are of two kinds. A file that changed while the scan
// ran is retryable: scanning it again closes the gap. An engine limit (a
// construct it cannot analyze, a file it cannot parse) recurs on every run of
// that engine over that file, so it describes the scanner, not the run.

// WarningRetryable reports whether scanning again closes a warning's gap.
func WarningRetryable(kind api.ScanWarningKind) bool {
	return kind == api.ScanWarningSourceMoved
}

// ScanGaps is one scan's coverage gaps after later scans are credited.
type ScanGaps struct {
	ScanID  string
	Scanner string
	// Moved are files that changed during the scan and no later complete scan
	// by the same scanner covered.
	Moved []string
	// Standing counts engine-limit warnings, and the files they touch.
	Standing      int
	StandingFiles int
}

// Open reports whether the scan still has a retryable gap.
func (g ScanGaps) Open() bool { return len(g.Moved) > 0 }

// RunCoverageGaps classifies every scan's warnings. A moved file is closed by
// a later scan from the same scanner that completed with complete coverage,
// covered the file, and did not see it move again.
func RunCoverageGaps(scans []api.CodeScan) []ScanGaps {
	out := make([]ScanGaps, 0, len(scans))
	for _, s := range scans {
		gaps := ScanGaps{ScanID: s.ID, Scanner: strings.TrimSpace(s.ScannerID)}
		standingFiles := map[string]bool{}
		for _, w := range s.Warnings {
			if WarningRetryable(w.Kind) {
				if file := strings.TrimSpace(w.File); !movedFileCovered(s, file, scans) {
					gaps.Moved = append(gaps.Moved, file)
				}
				continue
			}
			gaps.Standing++
			if file := strings.TrimSpace(w.File); file != "" {
				standingFiles[file] = true
			}
		}
		gaps.StandingFiles = len(standingFiles)
		sort.Strings(gaps.Moved)
		gaps.Moved = uniqueSorted(gaps.Moved)
		out = append(out, gaps)
	}
	return out
}

// ClosesMovedFiles reports whether a scan outside a run's bound set closes any
// of the run's open moved-file gaps, and so belongs bound to the run.
func ClosesMovedFiles(bound []api.CodeScan, candidate api.CodeScan) bool {
	for _, s := range bound {
		if s.ID == candidate.ID {
			return false
		}
	}
	withCandidate := append(append([]api.CodeScan(nil), bound...), candidate)
	before := RunCoverageGaps(bound)
	after := RunCoverageGaps(withCandidate)
	for i := range before {
		if len(after[i].Moved) < len(before[i].Moved) {
			return true
		}
	}
	return false
}

func movedFileCovered(moved api.CodeScan, file string, scans []api.CodeScan) bool {
	for _, later := range scans {
		if later.ID == moved.ID || later.ScannerID != moved.ScannerID || !later.CreatedAt.After(moved.CreatedAt) {
			continue
		}
		if later.Status != api.CodeScanStatusComplete || later.CoverageStatus != api.ScanCoverageComplete {
			if !onlyStandingGaps(later) {
				continue
			}
		}
		if later.TargetKind == api.ScanTargetPaths && (file == "" || !containsPath(later.TargetPaths, file)) {
			continue
		}
		if movedAgain(later, file) {
			continue
		}
		return true
	}
	return false
}

// onlyStandingGaps admits a later scan whose partial coverage comes only
// from engine limits: its reading of the moved file is still sound.
func onlyStandingGaps(s api.CodeScan) bool {
	if s.Status != api.CodeScanStatusComplete {
		return false
	}
	for _, w := range s.Warnings {
		if WarningRetryable(w.Kind) {
			return false
		}
	}
	return s.CoverageStatus == api.ScanCoveragePartial
}

func movedAgain(s api.CodeScan, file string) bool {
	for _, w := range s.Warnings {
		if WarningRetryable(w.Kind) && (strings.TrimSpace(w.File) == "" || strings.TrimSpace(w.File) == file) {
			return true
		}
	}
	return false
}

func containsPath(paths []string, file string) bool {
	for _, p := range paths {
		if strings.TrimSpace(p) == file {
			return true
		}
	}
	return false
}

func uniqueSorted(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
