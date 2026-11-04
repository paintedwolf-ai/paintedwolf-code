package workspace

import (
	"path/filepath"
	"strings"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func init() { execpkg.SetViewProcessRecorder(recorderForViewDirectory) }

// recorderForViewDirectory names the recorder of the mounted view holding dir. It compares paths
// only, because a stat here could wait on the view's own server.
func recorderForViewDirectory(dir string) func(pid int) error {
	clean := filepath.Clean(dir)
	var recorder func(pid int) error
	sharedBranchViews.Range(func(key, raw any) bool {
		st := raw.(branchViewState)
		if st.selection.Facts.PreparationKind != adapter.PreparationMounted || !viewsHold(st.views, clean) {
			return true
		}
		recorder = BranchProcessRecorder(key.(string))
		return false
	})
	return recorder
}

func viewsHold(views []*adapter.PreparedView, dir string) bool {
	for _, view := range views {
		if view == nil {
			continue
		}
		if holdsPath(view.PrimaryWorkPath, dir) {
			return true
		}
		for _, root := range view.Roots {
			if holdsPath(root.WorkspacePath, dir) {
				return true
			}
		}
	}
	return false
}

func holdsPath(root, dir string) bool {
	if root == "" {
		return false
	}
	clean := filepath.Clean(root)
	return dir == clean || strings.HasPrefix(dir, clean+string(filepath.Separator))
}
