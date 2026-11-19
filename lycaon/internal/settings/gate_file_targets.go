package settings

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sensitivepath"
)

// declaredFileTarget returns the highest-risk uncovered crossing.
func (g *RuleApprovalGate) declaredFileTarget(cfg ApprovalConfig, action hitl.ProposedAction) *gate.FileTarget {
	targets := g.declaredFileTargets(action)
	if len(targets) == 0 {
		return nil
	}
	// Strict reviews the write itself, so scratch authority stands for reads only.
	if cfg.Posture.ReviewsConfinedWrites() {
		for _, target := range targets {
			if target.Mode == gate.ModeWrite {
				target.WithinConfinement = false
			}
		}
	}
	pool := make([]*gate.FileTarget, 0, len(targets))
	for _, target := range targets {
		if !currentActionCoversTarget(action, target) && !grantedPathCoversTarget(g, cfg.Grants, action, target) {
			pool = append(pool, target)
		}
	}
	if len(pool) == 0 {
		pool = targets
	}
	worst := worstFileTarget(pool)
	if len(targets) == 1 {
		return worst
	}
	// Copy the target before adding batch display metadata.
	withCount := *worst
	withCount.OtherTargetCount = len(targets) - 1
	return &withCount
}

// worstFileTarget picks the highest-risk crossing among candidates.
func worstFileTarget(candidates []*gate.FileTarget) *gate.FileTarget {
	worst := candidates[0]
	worstRank := fileTargetSeverityRank(worst)
	for _, target := range candidates[1:] {
		if rank := fileTargetSeverityRank(target); rank > worstRank {
			worst, worstRank = target, rank
		}
	}
	return worst
}

// fileTargetSeverityRank orders crossings by disclosure severity.
func fileTargetSeverityRank(t *gate.FileTarget) int {
	rank := 0
	if t.OutsideRoots && !t.WithinConfinement {
		rank = 1
	}
	if t.Sensitive {
		rank = 2
	}
	if t.ProtectedSubject {
		rank = 3
	}
	return rank
}

// declaredFileWrites reports whether the action's files are write targets and
// whether it declares files at all. A command declares none before it runs,
// where confinement answers for the process; its prepared stream writes reach
// review as writes, like a native write's.
func declaredFileWrites(action hitl.ProposedAction) (write, declared bool) {
	if IsCommandToolName(action.Invocation.Tool) {
		return true, len(action.Mutations.FileChanges) > 0
	}
	return IsPathMutatingTool(action.Invocation.Tool), true
}

// declaredFileTargets returns every declared path crossing.
func (g *RuleApprovalGate) declaredFileTargets(action hitl.ProposedAction) []*gate.FileTarget {
	write, declared := declaredFileWrites(action)
	if action.Invocation.NativePathTargets != nil {
		declared = true
	}
	if !declared {
		return nil
	}
	var targets []*gate.FileTarget
	roots := workspaceRoots(action)
	paths := action.Invocation.Files
	if action.Invocation.NativePathTargets != nil {
		paths = make([]string, len(action.Invocation.NativePathTargets))
		for i, target := range action.Invocation.NativePathTargets {
			paths[i] = target.Path
		}
	}
	for i, raw := range paths {
		path := strings.TrimSpace(raw)
		// Worker-branch paths are an OAR reject.
		if path == "" || enginepaths.IsWorkerBranchPath(path) {
			continue
		}
		mode := gate.ModeRead
		catalogMode := sensitivepath.ModeRead
		pathWrite := write
		if action.Invocation.NativePathTargets != nil {
			pathWrite = action.Invocation.NativePathTargets[i].Write
		}
		if pathWrite {
			mode, catalogMode = gate.ModeWrite, sensitivepath.ModeWrite
		}
		path = actionFilePath(action, path)
		target := &gate.FileTarget{
			Path: path, Mode: mode,
			OutsideRoots: PathEscapesRoots(roots, path),
		}
		if target.OutsideRoots {
			// The card names the resolved spelling and, for reads, whether the
			// target exists; both come from the host's own resolution and probe.
			if len(action.Invocation.ResolvedFiles) == len(paths) {
				target.ResolvedPath = strings.TrimSpace(action.Invocation.ResolvedFiles[i])
			}
			if mode == gate.ModeRead {
				probe := target.ResolvedPath
				if probe == "" && filepath.IsAbs(path) {
					probe = path
				}
				if probe != "" {
					_, exists, _ := probeTargetFolder(probe)
					target.Exists = &exists
				}
			}
		}
		target.WithinConfinement = pathWithinFileAuthority(action, roots, path, pathWrite)
		// ClassifyResolved covers sensitive descendants and path aliases.
		if m, ok := g.sources.Locations.ClassifyResolved(path, catalogMode); ok {
			target.Sensitive = true
			target.CatalogID = m.ID
			target.CatalogTitle = m.Title
			target.ProtectedSubject = m.Protected
		}
		// Credential files ask on write inside roots too; overlays cannot remove the class.
		if mode == gate.ModeWrite && protectedpath.IsCredentialFile(path) {
			target.ProtectedSubject = true
		}
		// Protected writes and key material remain reviewable inside roots.
		subject := confine.ClassifyBlockedWrite(path)
		if subject.Kind == confine.WriteSubjectKeyMaterial ||
			(subject.Kind != confine.WriteSubjectOrdinary && mode == gate.ModeWrite) {
			target.ProtectedSubject = true
		}
		if target.OutsideRoots || target.ProtectedSubject {
			targets = append(targets, target)
		}
	}
	return targets
}

// actionFilePath projects a declared file into the session mount view before review.
func actionFilePath(action hitl.ProposedAction, path string) string {
	if !action.Execution.Contained.Active() {
		return path
	}
	if _, ok := confine.PrivateTempFor(action.Scope.SessionScratchRoot).Resolve(path); !ok {
		return path
	}
	c := action.Execution.Contained
	mapping := confine.ProjectPrivateTemp(action.Scope.ProjectID, action.Scope.SessionScratchRoot,
		workspaceRoots(action), c.GrantedWriteRoots, append(append([]string(nil), c.ReadRoots...), action.Scope.SessionOutputRoot))
	if backing, ok := mapping.Resolve(path); ok {
		return backing
	}
	return path
}

func pathWithinFileAuthority(action hitl.ProposedAction, roots []string, path string, write bool) bool {
	c := action.Execution.Contained
	if !write && confine.PathWithinRoots(path, c.ReadRoots) {
		return true
	}
	root, err := confine.NativeFilesystemRootForPath(action.Scope.ProjectID, roots, c.GrantedWriteRoots, action.Scope.SessionScratchRoot, path, write)
	return err == nil && root != ""
}
