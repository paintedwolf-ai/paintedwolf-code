package workerworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/tools"
)

const maxStatDigestBytes = 4 << 20

type matchedWorkspaceRoot struct {
	stateKey string
	rootID   string
	rootPath string
}

func handleWorkspaceRepoChange(s *Service, _ context.Context, ev repochange.Event) {
	if s == nil || strings.TrimSpace(ev.ProjectDir) == "" {
		return
	}
	matches := matchWorkspacesForProjectDir(&s.WorkspaceTrackerRegistry, ev.ProjectDir)
	if len(matches) == 0 {
		return
	}
	sourceReason := firstNonEmpty(string(ev.Source), "repochange")
	for _, m := range matches {
		switch ev.Kind {
		case repochange.WorktreeChanged:
			if len(ev.Changes) == 0 {
				s.RecordWatcherOverflow(m.stateKey, m.rootID, sourceReason)
				continue
			}
			for _, change := range ev.Changes {
				rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(change.Path), "/")))
				if change.Kind == repochange.WorktreeChangeResync || rel == "" || rel == "." {
					s.RecordWatcherOverflow(m.stateKey, m.rootID, sourceReason)
					continue
				}
				trunkEv := buildTrunkChangeFromRepoEvent(m.rootID, m.rootPath, rel, change.Kind)
				s.RecordLowerTrunkChange(m.stateKey, trunkEv)
			}
		case repochange.HeadMoved:
			if len(ev.Paths) == 0 {
				s.RecordWatcherOverflow(m.stateKey, m.rootID, "head_moved")
				continue
			}
			for _, p := range ev.Paths {
				rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(p), "/")))
				if rel == "" || rel == "." {
					s.RecordWatcherOverflow(m.stateKey, m.rootID, "head_moved")
					continue
				}
				trunkEv := buildTrunkChangeFromRepoEvent(m.rootID, m.rootPath, rel, repochange.WorktreeChangeStructural)
				s.RecordLowerTrunkChange(m.stateKey, trunkEv)
			}
		}
	}
}

func matchWorkspacesForProjectDir(r *WorkspaceTrackerRegistry, projectDir string) []matchedWorkspaceRoot {
	if r == nil {
		return nil
	}
	r.obsMu.RLock()
	defer r.obsMu.RUnlock()
	seen := make(map[*WorkspaceObservationState]bool)
	var out []matchedWorkspaceRoot
	for _, st := range r.trackers {
		if st == nil || st.Tracker == nil || seen[st] {
			continue
		}
		seen[st] = true
		rootID, rootPath, ok := matchStateRoot(st, projectDir)
		if !ok {
			continue
		}
		key := firstNonEmpty(st.JobID, st.ChildSessionID, st.BranchRoot, st.WorkspacePath)
		if key != "" {
			out = append(out, matchedWorkspaceRoot{stateKey: key, rootID: rootID, rootPath: rootPath})
		}
	}
	return out
}

func matchStateRoot(st *WorkspaceObservationState, eventDir string) (string, string, bool) {
	if st == nil {
		return "", "", false
	}
	if st.BranchRoot != "" && sameCanonicalPath(st.BranchRoot, eventDir) {
		return "", "", false
	}
	for _, r := range st.Roots {
		if r.Path != "" && sameCanonicalPath(r.Path, eventDir) {
			return firstNonEmpty(r.ID, "primary"), r.Path, true
		}
	}
	if st.WorkspacePath != "" && sameCanonicalPath(st.WorkspacePath, eventDir) {
		return "primary", st.WorkspacePath, true
	}
	return "", "", false
}

func sameCanonicalPath(a, b string) bool {
	ca, cb := canonicalPath(a), canonicalPath(b)
	return ca != "" && cb != "" && ca == cb
}

func canonicalPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if eval, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(eval)
	}
	return filepath.Clean(p)
}

func buildTrunkChangeFromRepoEvent(rootID, rootPath, rel string, kind repochange.WorktreeChangeKind) observation.TrunkChangeEvent {
	ev := observation.TrunkChangeEvent{
		Path: layeredfs.QualifiedPath{RootID: firstNonEmpty(rootID, "primary"), Rel: rel},
		Kind: observation.TrunkWriteInPlace,
	}
	if kind == repochange.WorktreeChangeStructural {
		ev.Kind = observation.TrunkAtomicReplace
	}
	if rootPath == "" {
		return ev
	}
	absPath := filepath.Join(rootPath, filepath.FromSlash(rel))
	info, err := os.Lstat(absPath)
	if os.IsNotExist(err) {
		ev.Kind = observation.TrunkDelete
		return ev
	}
	if err != nil {
		return ev
	}
	ev.NewSize = info.Size()
	ev.NewModTimeNS = info.ModTime().UnixNano()
	if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil {
		ev.NewIno = uint64(sys.Ino)
		ev.NewDev = uint64(sys.Dev)
	}
	if info.Mode().IsRegular() && info.Size() >= 0 && info.Size() <= maxStatDigestBytes {
		if f, oErr := os.Open(absPath); oErr == nil {
			defer func() { _ = f.Close() }()
			h := sha256.New()
			if n, cErr := io.Copy(h, io.LimitReader(f, maxStatDigestBytes+1)); cErr == nil && n <= maxStatDigestBytes {
				ev.NewSHA256 = hex.EncodeToString(h.Sum(nil))
				ev.NewSize = n
			}
		}
	}
	return ev
}

func collectTargetTrackers(r *WorkspaceTrackerRegistry, key string) []*observation.Tracker {
	if r == nil {
		return nil
	}
	r.obsMu.RLock()
	defer r.obsMu.RUnlock()
	var targets []*observation.Tracker
	if strings.TrimSpace(key) != "" {
		if st := lookupStateLocked(r, key); st != nil && st.Tracker != nil {
			targets = append(targets, st.Tracker)
		}
		return targets
	}
	seen := make(map[*observation.Tracker]bool)
	for _, st := range r.trackers {
		if st != nil && st.Tracker != nil && !seen[st.Tracker] {
			seen[st.Tracker] = true
			targets = append(targets, st.Tracker)
		}
	}
	return targets
}

func resolveObservationLayer(m *Service, tr *observation.Tracker, jobID string, qpath layeredfs.QualifiedPath, rawLayer string) layeredfs.LayerSource {
	switch strings.TrimSpace(rawLayer) {
	case "private":
		return layeredfs.LayerPrivate
	case "parent_checkpoint", "checkpoint", "parent":
		return layeredfs.LayerParent
	case "trunk":
		return layeredfs.LayerTrunk
	}
	if isTouchedForJob(m, jobID, qpath.Rel) {
		return layeredfs.LayerPrivate
	}
	if tr != nil {
		if latest, ok := tr.LatestObservationForPath(qpath); ok && latest.Layer == layeredfs.LayerPrivate {
			return layeredfs.LayerPrivate
		}
	}
	return layeredfs.LayerTrunk
}

func isTouchedForJob(m *Service, jobID, relPath string) bool {
	if m == nil || m.Touches == nil {
		return false
	}
	jobID = strings.TrimSpace(jobID)
	relPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(relPath)))
	if jobID == "" || relPath == "" || relPath == "." {
		return false
	}
	for _, p := range m.Touches.Paths(jobID) {
		if p == relPath {
			return true
		}
	}
	return false
}

func resolveFactsPath(
	workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	facts tools.WorkerFileObservationFacts,
) (layeredfs.QualifiedPath, string) {
	if rootID := strings.TrimSpace(facts.RootID); rootID != "" && rootID != "worker-branch" && strings.TrimSpace(facts.RelPath) != "" {
		rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(facts.RelPath), "/")))
		disk := strings.TrimSpace(facts.AbsPath)
		if disk == "" {
			_, disk = resolveQualifiedAndDiskPath(workspacePath, branchRoot, roots, rel)
		}
		return layeredfs.QualifiedPath{RootID: rootID, Rel: rel}, disk
	}
	qpath, disk := resolveQualifiedAndDiskPath(workspacePath, branchRoot, roots, firstNonEmpty(facts.RelPath, facts.AbsPath))
	if strings.TrimSpace(facts.AbsPath) != "" {
		disk = strings.TrimSpace(facts.AbsPath)
	}
	return qpath, disk
}

func resolveQualifiedAndDiskPath(
	workspacePath, branchRoot string,
	roots []projectroot.RootRef,
	rawPath string,
) (layeredfs.QualifiedPath, string) {
	rawPath = strings.TrimSpace(rawPath)
	if colon := strings.IndexByte(rawPath, ':'); colon > 0 && !filepath.IsAbs(rawPath) {
		rootID := strings.TrimSpace(rawPath[:colon])
		rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(rawPath[colon+1:]), "/")))
		for _, r := range roots {
			if r.ID == rootID || r.Label == rootID {
				return layeredfs.QualifiedPath{RootID: r.ID, Rel: rel}, filepath.Join(r.Path, filepath.FromSlash(rel))
			}
		}
		return layeredfs.QualifiedPath{RootID: rootID, Rel: rel}, ""
	}
	if strings.HasPrefix(rawPath, "@") {
		slash := strings.IndexByte(rawPath, '/')
		if slash > 1 {
			label := rawPath[1:slash]
			rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(rawPath[slash+1:], "/")))
			for _, r := range roots {
				if r.Label == label || r.ID == label {
					return layeredfs.QualifiedPath{RootID: r.ID, Rel: rel}, filepath.Join(r.Path, filepath.FromSlash(rel))
				}
			}
		}
	}
	rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(rawPath, "/")))
	baseDir := firstNonEmpty(branchRoot, workspacePath)
	var disk string
	if filepath.IsAbs(rawPath) {
		disk = filepath.Clean(rawPath)
		if baseDir != "" {
			if r, err := filepath.Rel(filepath.Clean(baseDir), disk); err == nil && !strings.HasPrefix(r, "..") {
				rel = filepath.ToSlash(r)
			}
		}
	} else if baseDir != "" {
		disk = filepath.Join(baseDir, filepath.FromSlash(rel))
	}
	return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}, disk
}
