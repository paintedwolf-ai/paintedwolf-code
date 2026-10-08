package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromoteRoots binds a worker task to primary/branch filesystem locations for merge.
type PromoteRoots struct {
	Roots      []projectroot.RootRef
	BranchRoot string
}

// PromoteRootsForTask resolves promote path mapping for a worker overlay job.
func PromoteRootsForTask(task *api.WorkerTask, roots []projectroot.RootRef) PromoteRoots {
	if task == nil {
		return PromoteRoots{}
	}
	branch := strings.TrimSpace(task.WorkspaceRoot)
	return PromoteRoots{
		Roots:      append([]projectroot.RootRef(nil), roots...),
		BranchRoot: branch,
	}
}

// BranchRel maps a model path to a path relative to the worker branch root.
func (p PromoteRoots) BranchRel(activeRootID, modelPath string) (branchRel, displayPath string, err error) {
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" {
		return "", "", projectroot.ErrPathEscape
	}
	if len(p.Roots) <= 1 {
		rel := filepath.ToSlash(filepath.Clean(modelPath))
		return rel, rel, nil
	}
	abs, root, err := projectroot.ResolveAbs(p.Roots, activeRootID, modelPath)
	if err != nil {
		return "", "", err
	}
	primary, err := projectroot.PrimaryRoot(p.Roots)
	if err != nil {
		return "", "", err
	}
	displayPath = projectroot.Qualify(primary, root, abs)
	scopeRel := projectroot.ScopeRel(root, abs)
	dir, err := projectroot.BranchDirForID(root.ID)
	if err != nil {
		return "", "", err
	}
	return filepath.ToSlash(filepath.Join(dir, scopeRel)), displayPath, nil
}

// ReadPairBytes loads exact primary and branch bytes for a qualified promote path.
func (p PromoteRoots) ReadPairBytes(task *api.WorkerTask, qualifiedPath string) (primary, branch []byte, branchPresent bool, err error) {
	qualifiedPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(qualifiedPath)))
	if qualifiedPath == "" {
		return nil, nil, false, nil
	}
	if len(p.Roots) <= 1 {
		return readPromotePairBytes(task.WorkspacePath, p.BranchRoot, qualifiedPath)
	}
	primaryAbs, branchAbs, err := p.fileAbs(task, qualifiedPath)
	if err != nil {
		return nil, nil, false, err
	}
	primaryData, err := readPromoteFile(primaryAbs)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, false, err
	}
	branchData, branchErr := readPromoteFile(branchAbs)
	switch {
	case branchErr == nil:
		branchPresent = true
	case os.IsNotExist(branchErr):
		branchPresent = false
	default:
		return nil, nil, false, branchErr
	}
	return primaryData, branchData, branchPresent, nil
}

func readPromotePairBytes(primaryDir, workspaceRoot, rel string) (primary, branch []byte, branchPresent bool, err error) {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	primary, err = readPromoteFile(filepath.Join(primaryDir, filepath.FromSlash(rel)))
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, false, err
	}
	branch, err = readPromoteFile(filepath.Join(workspaceRoot, filepath.FromSlash(rel)))
	switch {
	case err == nil:
		return primary, branch, true, nil
	case os.IsNotExist(err):
		return primary, nil, false, nil
	default:
		return nil, nil, false, err
	}
}

func (p PromoteRoots) fileAbs(task *api.WorkerTask, qualifiedPath string) (primaryAbs, branchAbs string, err error) {
	if task == nil {
		return "", "", projectroot.ErrNoProjectRoots
	}
	if len(p.Roots) <= 1 {
		rel := filepath.FromSlash(qualifiedPath)
		return filepath.Join(task.WorkspacePath, rel), filepath.Join(p.BranchRoot, rel), nil
	}
	abs, root, err := projectroot.ResolveAbs(p.Roots, task.WorkspaceRootID, qualifiedPath)
	if err != nil {
		return "", "", err
	}
	scopeRel := projectroot.ScopeRel(root, abs)
	dir, err := projectroot.BranchDirForID(root.ID)
	if err != nil {
		return "", "", err
	}
	return abs, filepath.Join(p.BranchRoot, dir, scopeRel), nil
}

// WritePrimaryBytes writes exact content to the correct source root.
func (p PromoteRoots) WritePrimaryBytes(task *api.WorkerTask, qualifiedPath string, content []byte) error {
	return p.writePrimaryBytes(task, qualifiedPath, content, nil, nil)
}

func (p PromoteRoots) writePrimaryBytes(task *api.WorkerTask, qualifiedPath string, content []byte, restoreMode *os.FileMode, verify func(fseffect.Target) error) error {
	qualifiedPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(qualifiedPath)))
	if qualifiedPath == "" || qualifiedPath == "." || sandbox.HasParentTraversal(qualifiedPath) {
		return nil
	}
	primaryAbs, _, err := p.fileAbs(task, qualifiedPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(primaryAbs), 0o750); err != nil {
		return err
	}
	primaryRoot, err := p.primaryRoot(task, primaryAbs)
	if err != nil {
		return err
	}
	if err := syncPromoteDirectoryTree(primaryRoot, filepath.Dir(primaryAbs)); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if restoreMode != nil {
		mode = restoreMode.Perm()
	}
	// The rooted location rejects symlinked parent components.
	primaryRel, err := filepath.Rel(primaryRoot, primaryAbs)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location:     fseffect.Location{Root: primaryRoot, Rel: primaryRel},
		Source:       bytes.NewReader(content),
		Mode:         mode,
		PreserveMode: restoreMode == nil,
		DirMode:      0o750,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			if verify != nil {
				return verify(target)
			}
			return nil
		},
	})
	return err
}

func (p PromoteRoots) primaryRoot(task *api.WorkerTask, primaryAbs string) (string, error) {
	if task == nil {
		return "", projectroot.ErrNoProjectRoots
	}
	if len(p.Roots) <= 1 {
		return filepath.Abs(task.WorkspacePath)
	}
	primaryAbs, err := filepath.Abs(primaryAbs)
	if err != nil {
		return "", err
	}
	for _, root := range p.Roots {
		rootAbs, absErr := filepath.Abs(root.Path)
		if absErr != nil {
			continue
		}
		rel, relErr := filepath.Rel(rootAbs, primaryAbs)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rootAbs, nil
		}
	}
	return "", projectroot.ErrNoProjectRoots
}

func syncPromoteDirectoryTree(root, leaf string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	current, err := filepath.Abs(leaf)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, current)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return projectroot.ErrPathEscape
	}
	for {
		dir, openErr := os.Open(current)
		if openErr != nil {
			return openErr
		}
		syncErr := fssync.File(dir)
		closeErr := dir.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if current == rootAbs {
			return nil
		}
		current = filepath.Dir(current)
	}
}

// WritePrimaryText encodes validated text in an explicitly selected representation.
func (p PromoteRoots) WritePrimaryText(task *api.WorkerTask, qualifiedPath, content, encoding string) error {
	raw, err := textfile.EncodeBounded(content, encoding,
		textfile.LimitsForRaw(promoteConflictMaxFileBytes))
	if err != nil {
		return err
	}
	return p.WritePrimaryBytes(task, qualifiedPath, raw)
}

func (p PromoteRoots) dropPrimary(task *api.WorkerTask, qualifiedPath string, verify func(fseffect.Target) error) error {
	qualifiedPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(qualifiedPath)))
	if qualifiedPath == "" || qualifiedPath == "." || sandbox.HasParentTraversal(qualifiedPath) {
		return nil
	}
	primaryAbs, _, err := p.fileAbs(task, qualifiedPath)
	if err != nil {
		return err
	}
	primaryRoot, err := p.primaryRoot(task, primaryAbs)
	if err != nil {
		return err
	}
	primaryRel, err := filepath.Rel(primaryRoot, primaryAbs)
	if err != nil {
		return err
	}
	err = fseffect.Remove(fseffect.RemoveRequest{
		Location:     fseffect.Location{Root: primaryRoot, Rel: primaryRel},
		BeforeCommit: verify,
	})
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
