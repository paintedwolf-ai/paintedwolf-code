package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

func projectConflictForSpill(c PromoteConflict) api.WorkerMergeConflict {
	return api.WorkerMergeConflict{
		Path:             c.Path,
		Reason:           c.Reason,
		Base:             c.Base,
		Primary:          c.Primary,
		Branch:           c.Branch,
		Summary:          append([]string(nil), c.Summary...),
		Hunks:            append([]api.WorkerMergeHunk(nil), c.Hunks...),
		OverlapWorkerIDs: append([]string(nil), c.OverlapJobIDs...),
		BranchDelta:      BranchDelta(c.Primary, c.Branch),
		BaseDelta:        BranchDelta(c.Base, c.Branch),
		ConflictTier:     ClassifyConflictTier(c),
	}
}

func buildPromoteSpillResult(out api.WorkerMergeResult, assessment PromoteAssessment) api.WorkerMergeResult {
	spill := out
	spill.Conflicts = nil
	for _, c := range assessment.Conflicts {
		spill.Conflicts = append(spill.Conflicts, projectConflictForSpill(c))
	}
	return spill
}

// WritePromoteSpill persists a full overlay assessment under the project host data dir.
// Returns the host-data-relative wire path (promote-spills/<id>.json) for agent read.
func WritePromoteSpill(hostDataDir, overlayID string, result api.WorkerMergeResult) (string, error) {
	hostDataDir = strings.TrimSpace(hostDataDir)
	overlayID = strings.TrimSpace(overlayID)
	if hostDataDir == "" || overlayID == "" {
		return "", nil
	}
	rel := tooloutput.PromoteSpillRelPath(overlayID)
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if tooloutput.SpillFileTooLarge(len(raw), 0) {
		return "", fmt.Errorf("promote spill exceeds %d byte cap", tooloutput.EffectiveMaxSpillFileBytes(0))
	}
	abs := filepath.Join(hostDataDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", err
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: hostDataDir, Rel: filepath.FromSlash(rel)},
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o750,
	}); err != nil {
		return "", err
	}
	return rel, nil
}

func (s *MergeService) attachPromoteSpill(hostDataDir, overlayID string, out *api.WorkerMergeResult, assessment PromoteAssessment) {
	if out == nil || len(assessment.Conflicts) == 0 {
		return
	}
	rel, err := WritePromoteSpill(hostDataDir, overlayID, buildPromoteSpillResult(*out, assessment))
	if err != nil || rel == "" {
		return
	}
	out.SpillPath = rel
}
