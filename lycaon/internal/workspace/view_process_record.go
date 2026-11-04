package workspace

import (
	"path/filepath"
	"strings"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func init() { execpkg.SetViewProcessRecorder(recorderForViewDirectory) }

// recorderForViewDirectory names the recorder of the mounted view holding dir. It never resolves dir,
// because a stat there could wait on the view's own server.
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

// holdsPath also accepts the root's canonical spelling, which launchers such as Git use. Only the
// root's parent is resolved: the root itself may be the mount.
func holdsPath(root, dir string) bool {
	if root == "" {
		return false
	}
	clean := filepath.Clean(root)
	if within(clean, dir) {
		return true
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	return err == nil && within(filepath.Join(parent, filepath.Base(clean)), dir)
}

func within(root, dir string) bool {
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}
