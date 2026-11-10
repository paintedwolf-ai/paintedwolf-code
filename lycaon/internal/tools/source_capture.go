package tools

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// SourceCaptureLocation binds a disk pin to its physical and project coordinates.
type SourceCaptureLocation struct {
	Disk        fseffect.Location
	RootID      string
	Path        string
	DisplayPath string
}

func (tc ToolContext) ResolveSourceCapture(sourceID string) (SourceCaptureLocation, bool) {
	identity, ok := strings.CutPrefix(sourceID, "disk:")
	if !ok {
		return SourceCaptureLocation{}, false
	}
	rootID, rel, ok := strings.Cut(identity, "/")
	if !ok || !filepath.IsLocal(rel) || rel == "." {
		return SourceCaptureLocation{}, false
	}
	var diskRoot string
	if rootID == "worker-branch" {
		diskRoot = tc.Source.WorkerBranchRoot
	} else {
		for _, root := range tc.Source.Roots {
			if root.ID == rootID {
				diskRoot = root.Path
				break
			}
		}
	}
	if diskRoot == "" {
		return SourceCaptureLocation{}, false
	}
	root, path, ok := tc.SourceLocation(filepath.Join(diskRoot, filepath.FromSlash(rel)))
	if !ok {
		return SourceCaptureLocation{}, false
	}
	primary, err := projectroot.PrimaryRoot(tc.Source.Roots)
	if err != nil {
		return SourceCaptureLocation{}, false
	}
	return SourceCaptureLocation{
		Disk:   fseffect.Location{Root: diskRoot, Rel: filepath.FromSlash(rel)},
		RootID: root.ID, Path: path,
		DisplayPath: projectroot.Qualify(primary, root, filepath.Join(root.Path, filepath.FromSlash(path))),
	}, true
}
