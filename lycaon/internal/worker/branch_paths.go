package worker

import (
	"bytes"
	"fmt"
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
	Roots        []projectroot.RootRef
	BranchRoot   string
	ActiveRootID string
}

// PromoteRootsForTask resolves promote path mapping for a worker overlay job.
func PromoteRootsForTask(task *api.WorkerTask, roots []projectroot.RootRef) PromoteRoots {
	if task == nil {
		return PromoteRoots{}
	}
	branch := strings.TrimSpace(task.WorkspaceRoot)
	return PromoteRoots{
		Roots:        append([]projectroot.RootRef(nil), roots...),
		BranchRoot:   branch,
		ActiveRootID: task.WorkspaceRootID,
	}
}

// ReadPairBytes loads exact primary and branch bytes for a qualified promote path.
func (p PromoteRoots) ReadPairBytes(task *api.WorkerTask, qualifiedPath string) (primary, branch []byte, branchPresent bool, err error) {
	qualifiedPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(qualifiedPath)))
	if qualifiedPath == "" {
		return nil, nil, false, nil
	}
	id, err := p.locateIdentity(task, qualifiedPath)
	if err != nil {
		return nil, nil, false, err
	}
	return p.ReadPairBytesAt(task, id.RootID, id.ScopeRel)
}

// ReadPairBytesAt loads exact primary and branch bytes for a structured root
// identity without re-resolving display strings.
func (p PromoteRoots) ReadPairBytesAt(task *api.WorkerTask, rootID, scopeRel string) (primary, branch []byte, branchPresent bool, err error) {
	id, err := p.identityAt(task, rootID, scopeRel)
	if err != nil {
		return nil, nil, false, err
	}
	primaryData, err := readPromoteFile(id.Primary)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, false, err
	}
	branchData, branchErr := readPromoteFile(id.Branch)
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

// promoteIdentity is the structured location of one promote path: the root it
// belongs to, its scope-relative path, and the absolute source and branch
// locations those resolve to.
type promoteIdentity struct {
	RootID   string
	ScopeRel string
	Root     projectroot.RootRef
	Primary  string
	Branch   string
}

// locateIdentity resolves a qualified promote path to its structured root
// identity and filesystem locations. Multi-root spellings follow the baseline
// grammar (bare rel paths are primary-relative, "@label/rel" addresses another
// attached root), so paths are parsed once instead of re-resolved against the
// task-active root.
func (p PromoteRoots) locateIdentity(task *api.WorkerTask, qualifiedPath string) (promoteIdentity, error) {
	if task == nil {
		return promoteIdentity{}, projectroot.ErrNoProjectRoots
	}
	if len(p.Roots) <= 1 {
		return p.identityAt(task, p.singleRootID(), qualifiedPath)
	}
	root, scopeRel, ok := projectroot.ParseBaselinePath(p.Roots, qualifiedPath)
	if !ok {
		return promoteIdentity{}, fmt.Errorf("promote path %q does not address an attached project root", qualifiedPath)
	}
	return p.identityAt(task, root.ID, scopeRel)
}

// identityAt maps a structured root identity to its source and branch locations.
func (p PromoteRoots) identityAt(task *api.WorkerTask, rootID, scopeRel string) (promoteIdentity, error) {
	if task == nil {
		return promoteIdentity{}, projectroot.ErrNoProjectRoots
	}
	scopeRel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(scopeRel)))
	if len(p.Roots) <= 1 {
		rel := filepath.FromSlash(scopeRel)
		id := promoteIdentity{
			ScopeRel: scopeRel,
			Primary:  filepath.Join(task.WorkspacePath, rel),
			Branch:   filepath.Join(p.BranchRoot, rel),
		}
		if len(p.Roots) == 1 {
			id.RootID, id.Root = p.Roots[0].ID, p.Roots[0]
		}
		return id, nil
	}
	root, ok := projectroot.RootRefByID(p.Roots, rootID)
	if !ok {
		return promoteIdentity{}, fmt.Errorf("unknown project root %q", rootID)
	}
	branchAbs, err := branchAbsFor(p, root.ID, scopeRel)
	if err != nil {
		return promoteIdentity{}, err
	}
	return promoteIdentity{
		RootID:   root.ID,
		ScopeRel: scopeRel,
		Root:     root,
		Primary:  filepath.Join(root.Path, filepath.FromSlash(scopeRel)),
		Branch:   branchAbs,
	}, nil
}

func (p PromoteRoots) singleRootID() string {
	if len(p.Roots) == 1 {
		return p.Roots[0].ID
	}
	return ""
}

// branchAbsFor maps a structured identity to its isolated branch location.
func branchAbsFor(p PromoteRoots, rootID, scopeRel string) (string, error) {
	if len(p.Roots) <= 1 {
		return filepath.Join(p.BranchRoot, filepath.FromSlash(scopeRel)), nil
	}
	dir, err := projectroot.BranchDirForID(rootID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.BranchRoot, dir, filepath.FromSlash(scopeRel)), nil
}

// fileAbs resolves a qualified promote path to its source and branch locations.
func (p PromoteRoots) fileAbs(task *api.WorkerTask, qualifiedPath string) (primaryAbs, branchAbs string, err error) {
	id, err := p.locateIdentity(task, qualifiedPath)
	if err != nil {
		return "", "", err
	}
	return id.Primary, id.Branch, nil
}

// WritePrimaryBytes writes exact content to the correct source root.
func (p PromoteRoots) WritePrimaryBytes(task *api.WorkerTask, qualifiedPath string, content []byte) error {
	qualifiedPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(qualifiedPath)))
	if qualifiedPath == "" || qualifiedPath == "." || sandbox.HasParentTraversal(qualifiedPath) {
		return nil
	}
	id, err := p.locateIdentity(task, qualifiedPath)
	if err != nil {
		return err
	}
	return p.writePrimaryBytesAt(task, id.RootID, id.ScopeRel, content, nil, nil)
}

// WritePrimaryBytesAt writes exact content to the structured source-root location.
func (p PromoteRoots) WritePrimaryBytesAt(task *api.WorkerTask, rootID, scopeRel string, content []byte) error {
	return p.writePrimaryBytesAt(task, rootID, scopeRel, content, nil, nil)
}

func (p PromoteRoots) writePrimaryBytes(task *api.WorkerTask, qualifiedPath string, content []byte, restoreMode *os.FileMode, verify func(fseffect.Target) error) error {
	qualifiedPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(qualifiedPath)))
	if qualifiedPath == "" || qualifiedPath == "." || sandbox.HasParentTraversal(qualifiedPath) {
		return nil
	}
	id, err := p.locateIdentity(task, qualifiedPath)
	if err != nil {
		return err
	}
	return p.writePrimaryBytesAt(task, id.RootID, id.ScopeRel, content, restoreMode, verify)
}

func (p PromoteRoots) writePrimaryBytesAt(task *api.WorkerTask, rootID, scopeRel string, content []byte, restoreMode *os.FileMode, verify func(fseffect.Target) error) error {
	scopeRel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(scopeRel)))
	if scopeRel == "" || scopeRel == "." || sandbox.HasParentTraversal(scopeRel) {
		return nil
	}
	id, err := p.identityAt(task, rootID, scopeRel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(id.Primary), 0o750); err != nil {
		return err
	}
	primaryRoot, err := p.primaryRootAt(task, id)
	if err != nil {
		return err
	}
	if err := syncPromoteDirectoryTree(primaryRoot, filepath.Dir(id.Primary)); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if restoreMode != nil {
		mode = restoreMode.Perm()
	}
	// The rooted location rejects symlinked parent components.
	primaryRel, err := filepath.Rel(primaryRoot, id.Primary)
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

// primaryRootAt returns the source root directory that contains the identity.
func (p PromoteRoots) primaryRootAt(task *api.WorkerTask, id promoteIdentity) (string, error) {
	if task == nil {
		return "", projectroot.ErrNoProjectRoots
	}
	if len(p.Roots) <= 1 {
		return filepath.Abs(task.WorkspacePath)
	}
	if id.RootID == "" {
		return "", projectroot.ErrNoProjectRoots
	}
	return filepath.Abs(id.Root.Path)
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
	id, err := p.locateIdentity(task, qualifiedPath)
	if err != nil {
		return err
	}
	return p.dropPrimaryAt(task, id.RootID, id.ScopeRel, verify)
}

// dropPrimaryAt removes the structured source-root location.
func (p PromoteRoots) dropPrimaryAt(task *api.WorkerTask, rootID, scopeRel string, verify func(fseffect.Target) error) error {
	scopeRel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(scopeRel)))
	if scopeRel == "" || scopeRel == "." || sandbox.HasParentTraversal(scopeRel) {
		return nil
	}
	id, err := p.identityAt(task, rootID, scopeRel)
	if err != nil {
		return err
	}
	primaryRoot, err := p.primaryRootAt(task, id)
	if err != nil {
		return err
	}
	primaryRel, err := filepath.Rel(primaryRoot, id.Primary)
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
