package projectpaths

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
)

// Resolved anchors a model path under an attached or approved root.
type Resolved struct {
	Abs         string
	DisplayPath string
	ScopeRel    string
	Root        projectroot.RootRef
	// External identifies host data, scratch and approved paths outside attached roots.
	External bool
	// Compressed marks host data that requires transparent decompression.
	Compressed bool
	// ToolOutput identifies retained observations under the host's spill roots.
	ToolOutput bool
}

// EffectLocation returns descriptor-relative path authority.
func (r Resolved) EffectLocation() fseffect.Location {
	return fseffect.Location{Root: r.Root.Path, Rel: filepath.FromSlash(r.ScopeRel)}
}

// GrantedAccessSource resolves project-scoped path authority.
type GrantedAccessSource func(rootSessionID, projectID, abs string, write bool) (Access, bool)

// Access is the approved filesystem subject a grant installed.
type Access struct {
	Path string
	Tree bool
}

var grantedAccess atomic.Pointer[GrantedAccessSource]

// SetGrantedAccessSource installs the granted-access lookup.
func SetGrantedAccessSource(fn GrantedAccessSource) {
	if fn == nil {
		grantedAccess.Store(nil)
		return
	}
	grantedAccess.Store(&fn)
}

// resolveGranted anchors scope on the approved path.
func resolveGranted(tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, bool) {
	abs := strings.TrimSpace(modelPath)
	if abs == "" || !filepath.IsAbs(abs) {
		// Grants apply only to absolute paths.
		return Resolved{}, false
	}
	// Canonical paths keep aliases within the granted tree.
	abs = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(abs)))
	rootSession := strings.TrimSpace(tctx.ParentSessionID)
	if rootSession == "" {
		rootSession = strings.TrimSpace(tctx.SessionID)
	}
	access, ok := approvedAccess(tctx, rootSession, abs, op != sandbox.PathOpRead)
	if !ok {
		return Resolved{}, false
	}
	anchor := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(access.Path)))
	if !access.Tree {
		// Missing parents need an existing effect anchor; authority stays on the leaf.
		anchor = existingEffectAnchor(filepath.Dir(anchor))
	}
	scopeRel, err := filepath.Rel(anchor, abs)
	if err != nil || scopeRel == ".." || strings.HasPrefix(scopeRel, ".."+string(filepath.Separator)) {
		return Resolved{}, false
	}
	if scopeRel == "." {
		scopeRel = ""
	}
	return Resolved{
		Abs:         abs,
		DisplayPath: abs,
		ScopeRel:    filepath.ToSlash(scopeRel),
		Root:        projectroot.RootRef{Path: anchor},
		External:    true,
	}, true
}

func approvedAccess(tctx tools.ToolContext, rootSession, abs string, write bool) (Access, bool) {
	accesses := append([]hitl.GrantedPathDelta(nil), tctx.ApprovedFileAccess...)
	if tctx.FileChangeReview != nil {
		accesses = append(accesses, tctx.PreparedFileAccess...)
	}
	for _, access := range accesses {
		// The frozen canonical path prevents symlink retargeting from redirecting approval.
		if access.Path == abs && (!write || access.Write) {
			return Access{Path: abs}, true
		}
	}
	if fn := grantedAccess.Load(); fn != nil {
		return (*fn)(rootSession, tctx.ProjectID, abs, write)
	}
	return Access{}, false
}

// controlPlaneReject refuses an absolute path inside the host's own state tree
// with the approval gate's code. Relative paths are answered by root resolution.
func controlPlaneReject(tctx tools.ToolContext, modelPath string, op sandbox.PathOp) *tools.ToolReject {
	abs := strings.TrimSpace(modelPath)
	if abs == "" || !filepath.IsAbs(abs) {
		return nil
	}
	abs = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(abs)))
	if !confine.ControlPlanePathDenied(abs, op != sandbox.PathOpRead, tctx.SessionScratchDir) {
		return nil
	}
	mode := "read"
	if op != sandbox.PathOpRead {
		mode = "write"
	}
	return &tools.ToolReject{
		Code: isolation.CodeControlPlaneDenied,
		Data: map[string]any{"path": filepath.ToSlash(abs), "mode": mode},
	}
}

// ResolveRead resolves modelPath for read/discovery tools and enforces read scope.
func ResolveRead(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string) (Resolved, error) {
	return resolve(ctx, b, tctx, modelPath, sandbox.PathOpRead)
}

// ResolveWrite enforces the shared mutation boundary.
func ResolveWrite(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string) (Resolved, error) {
	resolved, err := resolveMutationScope(ctx, b, tctx, modelPath)
	if err != nil {
		return Resolved{}, err
	}
	recordPrimaryMutation(ctx, tctx, resolved)
	return resolved, nil
}

// ResolveGitStage checks staging scope without applying content-write policy or recording file edits.
func ResolveGitStage(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string) (Resolved, error) {
	resolved, err := resolveMutationScope(ctx, b, tctx, modelPath)
	if err != nil {
		return Resolved{}, err
	}
	if resolved.External {
		return Resolved{}, mapResolveErr(fmt.Errorf("%w: %q", projectroot.ErrPathEscape, modelPath), modelPath)
	}
	if _, err := ResolveRead(ctx, b, tctx, modelPath); err != nil {
		return Resolved{}, err
	}
	return resolved, nil
}

func resolveMutationScope(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string) (Resolved, error) {
	if class, denied := repositoryMetadataClass(modelPath); denied {
		return Resolved{}, gitInternalsWriteReject(modelPath, class)
	}
	resolved, err := resolve(ctx, b, tctx, modelPath, sandbox.PathOpWrite)
	if err != nil {
		return Resolved{}, err
	}
	// Resolution can expose repository metadata hidden by its input spelling.
	if class, denied := repositoryMetadataClass(resolved.ScopeRel); denied {
		return Resolved{}, gitInternalsWriteReject(resolved.DisplayPath, class)
	}
	return resolved, nil
}

// recordPrimaryMutation records a primary-tree write for the open rewind checkpoint.
// Worker-branch writes are skipped; promote records those touches.
func recordPrimaryMutation(ctx context.Context, tctx tools.ToolContext, resolved Resolved) {
	if tctx.MutationRecorder == nil || resolved.External || strings.TrimSpace(tctx.WorkerBranchRoot) != "" {
		return
	}
	rel := strings.TrimSpace(resolved.ScopeRel)
	if rel == "" {
		return
	}
	tctx.MutationRecorder.RecordPrimaryMutation(ctx, tctx.SessionID, rel)
}

// repositoryMetadataClass names the .git content native tools never write;
// Git's own operations change it. Credential files are not refused here: their
// writes reach the approval gate as protected subjects.
func repositoryMetadataClass(path string) (string, bool) {
	if class, denied := tools.IsGitInternalsWritePath(path); denied {
		return class, true
	}
	if tools.IsRepositoryMetadataPath(path) {
		return "metadata", true
	}
	return "", false
}

func gitInternalsWriteReject(path, class string) error {
	return &tools.ToolReject{
		Code: "GIT_INTERNALS_WRITE_DENIED",
		Data: map[string]any{
			"path":  filepath.ToSlash(strings.TrimSpace(path)),
			"class": class,
		},
	}
}

func workspaceRoots(tctx tools.ToolContext) []string {
	out := make([]string, 0, len(tctx.Roots))
	for _, r := range tctx.Roots {
		if p := strings.TrimSpace(r.Path); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func resolve(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, error) {
	if reject := tools.ValidateAttachedRootsForAction(actionRootPaths(tctx)); reject != nil {
		return Resolved{}, reject
	}
	return resolveUnderAdmittedRoots(ctx, b, tctx, modelPath, op)
}

// resolveUnderAdmittedRoots applies the per-path rules once the call's
// attached roots are known to be admissible.
func resolveUnderAdmittedRoots(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, error) {
	if op == sandbox.PathOpRead {
		if hostResolved, ok, err := resolveHostDataRead(tctx, modelPath); ok || err != nil {
			return hostResolved, err
		}
	}
	// Scratch is the session's own folder, not project state, so a worker
	// without a branch writes there too.
	if scratchResolved, ok, err := resolveSessionScratch(ctx, b, tctx, modelPath, op); ok || err != nil {
		if err != nil {
			return Resolved{}, mapResolveErr(err, modelPath)
		}
		return scratchResolved, nil
	}
	// WorkerJobID without WorkerBranchRoot: reject non-reads (no mutation).
	if op != sandbox.PathOpRead && strings.TrimSpace(tctx.WorkerJobID) != "" && strings.TrimSpace(tctx.WorkerBranchRoot) == "" {
		return Resolved{}, workerWriteWithoutBranchReject(modelPath)
	}
	// No root, branch, or grant may answer for the control plane.
	if reject := controlPlaneReject(tctx, modelPath, op); reject != nil {
		return Resolved{}, reject
	}
	if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
		return resolveUnderBranch(ctx, b, tctx, branch, modelPath, op)
	}
	if len(tctx.Roots) == 0 {
		return Resolved{}, noRootsReject()
	}
	abs, root, err := projectroot.ResolveAbs(tctx.Roots, tctx.ActiveRootID, modelPath)
	if err != nil {
		// Grants are consulted only after attached roots decline the path.
		if granted, ok := resolveGranted(tctx, modelPath, op); ok {
			return granted, nil
		}
		if ordinary, ok, scopeErr := resolveFilesystemPath(ctx, b, tctx, modelPath, op); ok || scopeErr != nil {
			return ordinary, scopeErr
		}
		return Resolved{}, mapResolveErr(err, modelPath)
	}
	primary, err := projectroot.PrimaryRoot(tctx.Roots)
	if err != nil {
		return Resolved{}, noRootsReject()
	}
	scopeRel := projectroot.ScopeRel(root, abs)
	if b != nil {
		switch op {
		case sandbox.PathOpRead:
			if err := b.AssertReadScope(ctx, root.Path, scopeRel, tctx.ProfileID()); err != nil {
				return Resolved{}, err
			}
		case sandbox.PathOpWrite:
			if err := b.AssertPathAllowed(ctx, root.Path, scopeRel, sandbox.PathOpWrite); err != nil {
				return Resolved{}, err
			}
		default:
			if err := b.AssertPathAllowed(ctx, root.Path, scopeRel, op); err != nil {
				return Resolved{}, err
			}
		}
	}
	return Resolved{
		Abs:         abs,
		DisplayPath: projectroot.Qualify(primary, root, abs),
		ScopeRel:    scopeRel,
		Root:        root,
	}, nil
}

func actionRootPaths(tctx tools.ToolContext) []string {
	if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
		return []string{branch}
	}
	return workspaceRoots(tctx)
}

func resolveUnderBranch(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, branch, modelPath string, op sandbox.PathOp) (Resolved, error) {
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" {
		return Resolved{}, fmt.Errorf("path required")
	}
	if tctx.BranchWorkspace == nil {
		return Resolved{}, fmt.Errorf("worker branch workspace not configured")
	}
	var branchRel, displayPath string
	if filepath.IsAbs(modelPath) {
		var err error
		branchRel, err = filepath.Rel(filepath.Clean(branch), filepath.Clean(modelPath))
		if err != nil || branchRel == ".." || strings.HasPrefix(branchRel, ".."+string(filepath.Separator)) {
			return Resolved{}, mapResolveErr(fmt.Errorf("%w: %q", projectroot.ErrPathEscape, modelPath), modelPath)
		}
		displayPath = workerBranchDisplayPath(tctx, branchRel)
	} else {
		var err error
		branchRel, displayPath, err = projectroot.WorkerBranchRelative(tctx.Roots, tctx.ActiveRootID, modelPath)
		if err != nil {
			return Resolved{}, mapResolveErr(err, modelPath)
		}
	}
	scopeRel := filepath.ToSlash(filepath.Clean(branchRel))
	if scopeRel == "." {
		scopeRel = ""
	}
	if err := prepareBranchPath(ctx, tctx, scopeRel, op); err != nil {
		return Resolved{}, err
	}
	var abs string
	var err error
	if b != nil {
		abs, err = b.ResolveAbs(branch, branchRel)
	} else {
		abs, _, err = projectroot.ResolveAbs([]projectroot.RootRef{{
			ID: "worker-branch", Path: branch, IsPrimary: true,
		}}, "worker-branch", branchRel)
	}
	if err != nil {
		return Resolved{}, mapResolveErr(err, modelPath)
	}
	var root projectroot.RootRef
	if len(tctx.Roots) > 1 {
		if _, _, resolveErr := projectroot.ResolveAbs(tctx.Roots, tctx.ActiveRootID, displayPath); resolveErr != nil {
			return Resolved{}, mapResolveErr(resolveErr, modelPath)
		}
	}
	root = projectroot.RootRef{ID: "worker-branch", Path: branch, IsPrimary: true}
	if b != nil {
		switch op {
		case sandbox.PathOpRead:
			if err := b.AssertReadScope(ctx, branch, scopeRel, tctx.ProfileID()); err != nil {
				return Resolved{}, err
			}
		case sandbox.PathOpWrite:
			if err := b.AssertPathAllowed(ctx, branch, scopeRel, sandbox.PathOpWrite); err != nil {
				return Resolved{}, err
			}
		default:
			if err := b.AssertPathAllowed(ctx, branch, scopeRel, op); err != nil {
				return Resolved{}, err
			}
		}
	}
	display := displayPath
	if display == "" {
		display = scopeRel
		if primary, err := projectroot.PrimaryRoot(tctx.Roots); err == nil {
			display = projectroot.Qualify(primary, root, abs)
		}
	}
	return Resolved{
		Abs:         abs,
		DisplayPath: display,
		ScopeRel:    scopeRel,
		Root:        root,
	}, nil
}

func workerBranchDisplayPath(tctx tools.ToolContext, branchRel string) string {
	rel := filepath.ToSlash(filepath.Clean(branchRel))
	if len(tctx.Roots) <= 1 {
		return rel
	}
	branchDir, rest, _ := strings.Cut(rel, "/")
	for _, root := range tctx.Roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		if err == nil && dir == branchDir {
			if rest == "" {
				return "@" + root.Label
			}
			return "@" + root.Label + "/" + rest
		}
	}
	return rel
}

func prepareBranchPath(ctx context.Context, tctx tools.ToolContext, scopeRel string, op sandbox.PathOp) error {
	branch := tctx.BranchWorkspace
	if branch == nil {
		return fmt.Errorf("worker branch workspace not configured")
	}
	if err := branch.ValidateMeta(ctx); err != nil {
		return err
	}
	rel := strings.TrimSpace(scopeRel)
	if rel == "" || rel == "." {
		return nil
	}
	if op == sandbox.PathOpWrite {
		return branch.EnsureParents(ctx, rel)
	}
	return nil
}

// ResolveMisplacedSpill resolves a read path that embeds one exact
// content-addressed spill reference under another prefix, such as a root
// label, to that spill. The digest names the content, so the reference is
// unambiguous wherever it appears.
func ResolveMisplacedSpill(tctx tools.ToolContext, modelPath string) (Resolved, bool) {
	refs := tooloutput.SpillPaths(filepath.ToSlash(modelPath))
	if len(refs) != 1 || refs[0] == filepath.ToSlash(strings.TrimSpace(modelPath)) {
		return Resolved{}, false
	}
	resolved, ok, err := resolveHostDataRead(tctx, refs[0])
	if !ok || err != nil {
		return Resolved{}, false
	}
	return resolved, true
}

// resolveHostDataRead exposes allowlisted host spill paths.
func resolveHostDataRead(tctx tools.ToolContext, modelPath string) (Resolved, bool, error) {
	host := strings.TrimSpace(tctx.HostDataDir)
	if host == "" {
		return Resolved{}, false, nil
	}
	scopeRel, ok := tooloutput.AgentWireSpillScopeRel(host, modelPath)
	if !ok {
		return Resolved{}, false, nil
	}
	abs := filepath.Join(filepath.Clean(host), filepath.FromSlash(scopeRel))
	return Resolved{
		Abs:         abs,
		DisplayPath: scopeRel,
		ScopeRel:    scopeRel,
		Root:        projectroot.RootRef{ID: "host-data", Path: filepath.Clean(host), IsPrimary: false},
		External:    true,
		Compressed:  tooloutput.IsBlobstoreCompressedRel(scopeRel),
		ToolOutput:  strings.HasPrefix(scopeRel, tooloutput.ToolOutputSpillDir+"/") || strings.HasPrefix(scopeRel, tooloutput.PromoteSpillDir+"/"),
	}, true, nil
}

func mapResolveErr(err error, modelPath string) error {
	switch {
	case errors.Is(err, projectroot.ErrNoProjectRoots):
		return noRootsReject()
	case errors.Is(err, projectroot.ErrUnknownRootLabel):
		return &tools.ToolReject{
			Code: "UNKNOWN_ROOT_LABEL",
			Data: map[string]any{"path": modelPath, "reason": err.Error()},
		}
	case errors.Is(err, projectroot.ErrPathEscape):
		return &tools.ToolReject{
			Code: "SURVEY_PATH_ESCAPE",
			Data: map[string]any{"path": modelPath, "reason": err.Error()},
		}
	default:
		return err
	}
}

func noRootsReject() error {
	return &tools.ToolReject{Code: "PROJECT_HAS_NO_ROOTS", Data: map[string]any{}}
}

func workerWriteWithoutBranchReject(path string) error {
	return &tools.ToolReject{Code: "WORKER_WRITE_WITHOUT_BRANCH", Data: map[string]any{"path": filepath.ToSlash(strings.TrimSpace(path))}}
}

// UnionDiscoveryRoots returns roots for a union walk when modelPath is a union sentinel.
func UnionDiscoveryRoots(ctx context.Context, tctx tools.ToolContext, modelPath string) ([]projectroot.RootRef, error) {
	if reject := tools.ValidateAttachedRootsForAction(actionRootPaths(tctx)); reject != nil {
		return nil, reject
	}
	if len(tctx.Roots) == 0 {
		return nil, noRootsReject()
	}
	if projectroot.IsUnionDiscoveryPath(modelPath) {
		if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
			if len(tctx.Roots) > 1 {
				refs := make([]projectroot.RootRef, 0, len(tctx.Roots))
				for _, r := range tctx.Roots {
					dir, err := projectroot.BranchDirForID(r.ID)
					if err != nil {
						continue
					}
					refs = append(refs, projectroot.RootRef{
						ID: r.ID, Path: filepath.Join(branch, dir), Label: r.Label, IsPrimary: r.IsPrimary,
					})
				}
				if len(refs) > 0 {
					return refs, nil
				}
			}
			return []projectroot.RootRef{{ID: "worker-branch", Path: branch, IsPrimary: true}}, nil
		}
		return tctx.Roots, nil
	}
	res, err := ResolveRead(ctx, nil, tctx, modelPath)
	if err != nil {
		return nil, err
	}
	return []projectroot.RootRef{res.Root}, nil
}

// QualifyAbs formats an absolute path with multi-root display qualifiers.
func QualifyAbs(tctx tools.ToolContext, root projectroot.RootRef, abs string) string {
	primary, err := projectroot.PrimaryRoot(tctx.Roots)
	if err != nil {
		return abs
	}
	return projectroot.Qualify(primary, root, abs)
}

// CommandCwd resolves a process directory within its attached root or session scratch.
func CommandCwd(ctx context.Context, tctx tools.ToolContext, cwdArg string) (abs, display string, err error) {
	if reject := tools.ValidateAttachedRootsForAction(actionRootPaths(tctx)); reject != nil {
		return "", "", reject
	}
	cwdClean := strings.TrimSpace(cwdArg)
	if _, scratch := scratchRel(tctx, cwdClean); scratch {
		return commandCwdInScratch(ctx, tctx, cwdClean)
	}
	if branch := strings.TrimSpace(tctx.WorkerBranchRoot); branch != "" {
		return commandCwdUnderBranch(ctx, tctx, branch, cwdArg)
	}
	if len(tctx.Roots) == 0 {
		return "", "", noRootsReject()
	}
	if cwdClean == "" {
		return tctx.ActiveRootPath(), ".", nil
	}
	resolved, _, resolveErr := projectroot.ResolveAbs(tctx.Roots, tctx.ActiveRootID, cwdClean)
	if resolveErr != nil {
		return "", "", &tools.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.IsDir() {
		return "", "", &tools.ToolReject{
			Code: "CWD_NOT_DIRECTORY",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	return resolved, cwdArg, nil
}

// commandCwdInScratch runs a process in the session's scratch folder. A
// verification check never does: its receipt describes the project.
func commandCwdInScratch(ctx context.Context, tctx tools.ToolContext, cwdArg string) (abs, display string, err error) {
	if tctx.VerificationCheck {
		return "", "", &tools.ToolReject{
			Code: "CWD_SCRATCH_NOT_VERIFICATION",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	res, _, err := resolveSessionScratch(ctx, nil, tctx, cwdArg, sandbox.PathOpRead)
	if err != nil {
		var reject *tools.ToolReject
		if errors.As(err, &reject) && reject.Code == tools.SessionScratchUnavailableCode {
			return "", "", reject
		}
		return "", "", &tools.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	info, statErr := os.Stat(res.Abs)
	if statErr != nil || !info.IsDir() {
		return "", "", &tools.ToolReject{
			Code: "CWD_NOT_DIRECTORY",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	return res.Abs, res.DisplayPath, nil
}

func commandCwdUnderBranch(ctx context.Context, tctx tools.ToolContext, branch, cwdArg string) (abs, display string, err error) {
	cwdArg = strings.TrimSpace(cwdArg)
	if cwdArg == "" {
		return branch, ".", nil
	}
	if filepath.IsAbs(cwdArg) {
		return "", "", &tools.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	if tctx.BranchWorkspace == nil {
		return "", "", fmt.Errorf("worker branch workspace not configured")
	}
	branchRel, displayPath, mapErr := projectroot.WorkerBranchRelative(tctx.Roots, tctx.ActiveRootID, cwdArg)
	if mapErr != nil {
		return "", "", &tools.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	scopeRel := filepath.ToSlash(filepath.Clean(branchRel))
	if scopeRel == ".." || strings.HasPrefix(scopeRel, "../") {
		return "", "", &tools.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	if scopeRel == "." {
		scopeRel = ""
	}
	if branchErr := prepareBranchPath(ctx, tctx, scopeRel, sandbox.PathOpRead); branchErr != nil {
		if os.IsNotExist(branchErr) || errors.Is(branchErr, os.ErrNotExist) {
			return "", "", &tools.ToolReject{
				Code: "CWD_NOT_DIRECTORY",
				Data: map[string]any{"cwd": cwdArg},
			}
		}
		return "", "", branchErr
	}
	abs = filepath.Join(branch, filepath.FromSlash(scopeRel))
	rel, relErr := filepath.Rel(branch, abs)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", &tools.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	info, statErr := os.Stat(abs)
	if statErr != nil || !info.IsDir() {
		return "", "", &tools.ToolReject{
			Code: "CWD_NOT_DIRECTORY",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	display = displayPath
	if scopeRel == "" {
		display = "."
	} else if display == "" {
		display = scopeRel
	}
	return abs, display, nil
}

// resolveSessionScratch answers a path in the invoking session's own scratch
// folder: an @scratch address, or an absolute path inside the folder. Both
// spellings reach one scope check, and a link out of the folder is an escape.
func resolveSessionScratch(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, bool, error) {
	rel, addressed := scratchRel(tctx, modelPath)
	if !addressed {
		return Resolved{}, false, nil
	}
	dir := strings.TrimSpace(tctx.SessionScratchDir)
	if dir == "" {
		return Resolved{}, false, sessionScratchUnavailableReject(modelPath)
	}
	abs, err := projectroot.ScratchPath(dir, rel)
	if err != nil {
		return Resolved{}, false, err
	}
	if !confine.PathAtOrUnder(fspath.CanonicalPath(abs), fspath.CanonicalPath(dir)) {
		return Resolved{}, false, fmt.Errorf("%w: %q", projectroot.ErrPathEscape, modelPath)
	}
	scopeRel, err := filepath.Rel(filepath.Clean(dir), abs)
	if err != nil {
		return Resolved{}, false, err
	}
	scopeRel = filepath.ToSlash(scopeRel)
	display := "@" + projectroot.VirtualScratchLabel
	if scopeRel == "." {
		scopeRel = ""
	} else {
		display += "/" + scopeRel
	}
	if b != nil {
		if op == sandbox.PathOpRead {
			err = b.AssertReadScope(ctx, dir, scopeRel, tctx.ProfileID())
		} else {
			err = b.AssertPathAllowed(ctx, dir, scopeRel, op)
		}
		if err != nil {
			return Resolved{}, false, err
		}
	}
	return Resolved{
		Abs:         abs,
		DisplayPath: display,
		ScopeRel:    scopeRel,
		Root:        projectroot.RootRef{ID: projectroot.VirtualScratchLabel, Label: projectroot.VirtualScratchLabel, Path: filepath.Clean(dir)},
		External:    true,
	}, true, nil
}

// scratchRel reports whether path names the session's scratch folder, by
// address or by an absolute path inside it, and its place in that folder.
func scratchRel(tctx tools.ToolContext, path string) (string, bool) {
	if rel, ok := projectroot.ScratchAddress(path); ok {
		return rel, true
	}
	return scratchRelOfAbs(strings.TrimSpace(tctx.SessionScratchDir), path)
}

// scratchRelOfAbs returns an absolute path's place inside the scratch folder,
// comparing canonical forms so an alias of the folder still resolves there.
func scratchRelOfAbs(dir, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if dir == "" || !filepath.IsAbs(raw) {
		return "", false
	}
	rel, err := filepath.Rel(fspath.CanonicalPath(dir), fspath.CanonicalPath(raw))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func sessionScratchUnavailableReject(path string) error {
	return &tools.ToolReject{
		Code: tools.SessionScratchUnavailableCode,
		Data: map[string]any{"path": filepath.ToSlash(strings.TrimSpace(path))},
	}
}
