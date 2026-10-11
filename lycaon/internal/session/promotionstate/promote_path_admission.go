package promotionstate

import (
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// FilterOverlayPromoteCandidatePaths excludes host metadata and retains changed ignored files.
func FilterOverlayPromoteCandidatePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if OmitOverlayPromotePath(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// OmitOverlayPromotePath excludes host metadata and the policy governing the worker.
func OmitOverlayPromotePath(rel string) bool {
	rel = enginepaths.NormalizeRepoRel(rel)
	if rel == "" {
		return true
	}
	engineDir := settingsoverlay.DirName()
	if rel == engineDir || strings.HasPrefix(rel, engineDir+"/") {
		return true
	}
	for _, prefix := range []string{
		tooloutput.PromoteSpillDir,
		tooloutput.ToolOutputSpillDir,
		tooloutput.AttachmentSpillDir,
	} {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return sandbox.ShouldSkipDir(rel, path.Base(rel))
}
