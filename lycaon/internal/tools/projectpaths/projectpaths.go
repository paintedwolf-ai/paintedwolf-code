package projectpaths

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
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

// controlPlaneReject refuses an absolute path inside the host's own state tree
// with the approval gate's code. Relative paths are answered by root resolution.
func controlPlaneReject(tctx tools.ToolContext, modelPath string, op sandbox.PathOp) *toolrejection.ToolReject {
	abs := strings.TrimSpace(modelPath)
	if abs == "" || !filepath.IsAbs(abs) {
		return nil
	}
	abs = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(abs)))
	if !confine.ControlPlanePathDenied(abs, op != sandbox.PathOpRead, tctx.Host.SessionScratchDir) {
		return nil
	}
	mode := "read"
	if op != sandbox.PathOpRead {
		mode = "write"
	}
	return &toolrejection.ToolReject{
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
	if tctx.Source.MutationRecorder == nil || resolved.External || strings.TrimSpace(tctx.Source.WorkerBranchRoot) != "" {
		return
	}
	rel := strings.TrimSpace(resolved.ScopeRel)
	if rel == "" {
		return
	}
	tctx.Source.MutationRecorder.RecordPrimaryMutation(ctx, tctx.Identity.SessionID, rel)
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
	return &toolrejection.ToolReject{
		Code: "GIT_INTERNALS_WRITE_DENIED",
		Data: map[string]any{
			"path":  filepath.ToSlash(strings.TrimSpace(path)),
			"class": class,
		},
	}
}

func workspaceRoots(tctx tools.ToolContext) []string {
	out := make([]string, 0, len(tctx.Source.Roots))
	for _, r := range tctx.Source.Roots {
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
	if op != sandbox.PathOpRead && strings.TrimSpace(tctx.Identity.WorkerJobID) != "" && strings.TrimSpace(tctx.Source.WorkerBranchRoot) == "" {
		return Resolved{}, workerWriteWithoutBranchReject(modelPath)
	}
	// No root, branch, or grant may answer for the control plane.
	if reject := controlPlaneReject(tctx, modelPath, op); reject != nil {
		return Resolved{}, reject
	}
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		return resolveUnderBranch(ctx, b, tctx, branch, modelPath, op)
	}
	if len(tctx.Source.Roots) == 0 {
		return Resolved{}, noRootsReject()
	}
	abs, root, err := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, modelPath)
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
	primary, err := projectroot.PrimaryRoot(tctx.Source.Roots)
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
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		return []string{branch}
	}
	return workspaceRoots(tctx)
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
	host := strings.TrimSpace(tctx.Host.HostDataDir)
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
	var scope *sandbox.ScopeError
	if errors.As(err, &scope) || errors.Is(err, sandbox.ErrPathEscape) {
		return &toolrejection.ToolReject{Code: "SURVEY_PATH_ESCAPE", Data: map[string]any{"path": modelPath, "reason": err.Error()}}
	}
	switch {
	case errors.Is(err, projectroot.ErrNoProjectRoots):
		return noRootsReject()
	case errors.Is(err, projectroot.ErrUnknownRootLabel):
		return &toolrejection.ToolReject{
			Code: "UNKNOWN_ROOT_LABEL",
			Data: map[string]any{"path": modelPath, "reason": err.Error()},
		}
	case errors.Is(err, projectroot.ErrPathEscape):
		return &toolrejection.ToolReject{
			Code: "SURVEY_PATH_ESCAPE",
			Data: map[string]any{"path": modelPath, "reason": err.Error()},
		}
	default:
		return err
	}
}

func noRootsReject() error {
	return &toolrejection.ToolReject{Code: "PROJECT_HAS_NO_ROOTS", Data: map[string]any{}}
}

func workerWriteWithoutBranchReject(path string) error {
	return &toolrejection.ToolReject{Code: "WORKER_WRITE_WITHOUT_BRANCH", Data: map[string]any{"path": filepath.ToSlash(strings.TrimSpace(path))}}
}

// UnionDiscoveryRoots returns roots for a union walk when modelPath is a union sentinel.
func UnionDiscoveryRoots(ctx context.Context, tctx tools.ToolContext, modelPath string) ([]projectroot.RootRef, error) {
	if reject := tools.ValidateAttachedRootsForAction(actionRootPaths(tctx)); reject != nil {
		return nil, reject
	}
	if len(tctx.Source.Roots) == 0 {
		return nil, noRootsReject()
	}
	if projectroot.IsUnionDiscoveryPath(modelPath) {
		if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
			if len(tctx.Source.Roots) > 1 {
				refs := make([]projectroot.RootRef, 0, len(tctx.Source.Roots))
				for _, r := range tctx.Source.Roots {
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
		return tctx.Source.Roots, nil
	}
	res, err := ResolveRead(ctx, nil, tctx, modelPath)
	if err != nil {
		return nil, err
	}
	return []projectroot.RootRef{res.Root}, nil
}

// QualifyAbs formats an absolute path with multi-root display qualifiers.
func QualifyAbs(tctx tools.ToolContext, root projectroot.RootRef, abs string) string {
	primary, err := projectroot.PrimaryRoot(tctx.Source.Roots)
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
	if branch := strings.TrimSpace(tctx.Source.WorkerBranchRoot); branch != "" {
		return commandCwdUnderBranch(ctx, tctx, branch, cwdArg)
	}
	if len(tctx.Source.Roots) == 0 {
		return "", "", noRootsReject()
	}
	if cwdClean == "" {
		return tctx.ActiveRootPath(), ".", nil
	}
	resolved, _, resolveErr := projectroot.ResolveAbs(tctx.Source.Roots, tctx.Source.ActiveRootID, cwdClean)
	if resolveErr != nil {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.IsDir() {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_NOT_DIRECTORY",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	return resolved, cwdArg, nil
}

// commandCwdInScratch runs a process in the session's scratch folder. A
// verification check never does: its receipt describes the project.
func commandCwdInScratch(ctx context.Context, tctx tools.ToolContext, cwdArg string) (abs, display string, err error) {
	if tctx.Execution.VerificationCheck {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_SCRATCH_NOT_VERIFICATION",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	res, _, err := resolveSessionScratch(ctx, nil, tctx, cwdArg, sandbox.PathOpRead)
	if err != nil {
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) && reject.Code == tools.SessionScratchUnavailableCode {
			return "", "", reject
		}
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_OUT_OF_SCOPE",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	info, statErr := os.Stat(res.Abs)
	if statErr != nil || !info.IsDir() {
		return "", "", &toolrejection.ToolReject{
			Code: "CWD_NOT_DIRECTORY",
			Data: map[string]any{"cwd": cwdArg},
		}
	}
	return res.Abs, res.DisplayPath, nil
}

// resolveSessionScratch answers a path in the invoking session's own scratch
// folder: an @scratch address, or an absolute path inside the folder. Both
// spellings reach one scope check, and a link out of the folder is an escape.
func resolveSessionScratch(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, bool, error) {
	rel, addressed := scratchRel(tctx, modelPath)
	if !addressed {
		return Resolved{}, false, nil
	}
	dir := strings.TrimSpace(tctx.Host.SessionScratchDir)
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
	return scratchRelOfAbs(strings.TrimSpace(tctx.Host.SessionScratchDir), path)
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
	return &toolrejection.ToolReject{
		Code: tools.SessionScratchUnavailableCode,
		Data: map[string]any{"path": filepath.ToSlash(strings.TrimSpace(path))},
	}
}
