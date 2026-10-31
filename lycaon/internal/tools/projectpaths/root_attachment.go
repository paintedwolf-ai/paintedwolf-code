package projectpaths

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// rootLabel returns the label of an @label or @label/... spelling.
func rootLabel(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "@")
	if !ok {
		return "", false
	}
	label, _, _ := strings.Cut(rest, "/")
	return label, label != ""
}

// declinedRootReject answers a path no attached root resolved. A spelling a
// root change retired refuses as stale, so an old folder path never reaches
// grant or approval review as if it were an arbitrary host path; an unknown
// label names itself and the folders that resolve now. Other paths return nil.
func declinedRootReject(tctx tools.ToolContext, modelPath string, cause error) error {
	view := tctx.Source.RootHistory
	if label, ok := rootLabel(modelPath); ok {
		if retired, stale := view.StaleLabel(tctx.Source.Roots, label); stale {
			return tools.RootNotAttachedReject(tctx, modelPath, retired)
		}
		if errors.Is(cause, projectroot.ErrUnknownRootLabel) {
			return &toolrejection.ToolReject{Code: "UNKNOWN_ROOT_LABEL", Data: map[string]any{
				"path": modelPath, "root_label": label, "reason": cause.Error(), "attached_folders": tools.AttachedFolders(tctx),
			}}
		}
		return nil
	}
	if retired, stale := view.StalePath(tctx.Source.Roots, modelPath); stale {
		return tools.RootNotAttachedReject(tctx, filepath.Clean(modelPath), retired)
	}
	return nil
}

// rootResolveErr maps a root resolution failure: retired and unknown labels
// through declinedRootReject, everything else through mapResolveErr.
func rootResolveErr(tctx tools.ToolContext, err error, modelPath string) error {
	if reject := declinedRootReject(tctx, modelPath, err); reject != nil {
		return reject
	}
	return mapResolveErr(err, modelPath)
}

// currentRootReject refuses an effect under a root removed or moved after
// the invocation's context was captured; settled effects keep their facts.
func currentRootReject(tctx tools.ToolContext, root projectroot.RootRef, path string) error {
	if retired, ok := tctx.Source.RootHistory.RetiredSince(root); ok {
		return tools.RootNotAttachedReject(tctx, path, retired)
	}
	return nil
}
