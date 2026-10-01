package enginepaths

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// WorkerBranchesDirName holds worker branch copies outside project roots.
const WorkerBranchesDirName = "worker-branches"

// WorkerSeedsDirName holds rebuildable bridge generations only when central
// storage enables copy-on-write worker branches.
const WorkerSeedsDirName = "worker-seeds"

// WorkerBranchPathSegment is the layout marker in every absolute worker-branch path.
// The config-root leaf is omitted so every engine state root matches.
const WorkerBranchPathSegment = "/" + WorkerBranchesDirName + "/"

// WorkerBranchesRootUnder returns the worker-branch base directory under an engine state root.
func WorkerBranchesRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), WorkerBranchesDirName)
}

// WorkerSeedsRootUnder returns the central worker-seed base under an engine state root.
func WorkerSeedsRootUnder(stateRoot string) string {
	return filepath.Join(strings.TrimSpace(stateRoot), WorkerSeedsDirName)
}

// ProjectSeedDir returns the central seed directory for one source root.
func ProjectSeedDir(seedRoot, projectDir string) string {
	return filepath.Join(strings.TrimSpace(seedRoot), ProjectKey(projectDir))
}

// ProjectBranchDir returns the branch parent keyed by one source root.
func ProjectBranchDir(branchRoot, projectDir string) string {
	return filepath.Join(strings.TrimSpace(branchRoot), ProjectKey(projectDir))
}

// JobBranchDir returns the worker-branch root for a single write-worker job.
func JobBranchDir(branchRoot, projectDir, jobID string) string {
	return filepath.Join(ProjectBranchDir(branchRoot, projectDir), strings.TrimSpace(jobID))
}

// JobMetaDirSuffix names the host-only sibling of a job tree.
const JobMetaDirSuffix = ".meta"

// MetaDirForBranchRoot maps a live branch tree path to its sibling .meta dir.
func MetaDirForBranchRoot(branchTreeRoot string) string {
	return strings.TrimSpace(branchTreeRoot) + JobMetaDirSuffix
}

// IsJobMetaDirName reports a `{job-id}.meta` sibling directory name.
func IsJobMetaDirName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && strings.HasSuffix(name, JobMetaDirSuffix)
}

// ProjectKey derives a stable filesystem key from a source root.
func ProjectKey(projectDir string) string {
	abs := strings.TrimSpace(projectDir)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(sum[:])[:16]
}

// NormalizeRepoRel canonicalizes a repo-relative path for prefix checks.
func NormalizeRepoRel(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	for strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}
	return strings.TrimPrefix(path, "/")
}

// IsWorkerBranchPath reports a path under an engine worker-branch copy.
func IsWorkerBranchPath(path string) bool {
	p := filepath.ToSlash(strings.TrimSpace(path))
	if p == "" {
		return false
	}
	return strings.Contains(p, WorkerBranchPathSegment)
}

// WorkerBranchDisplayRel returns the repo-relative suffix of a worker-branch
// path (`src/main.rs`), or "." when the path is the branch root itself.
// Paths that are not worker-branch paths pass through NormalizeRepoRel.
func WorkerBranchDisplayRel(path string) string {
	p := filepath.ToSlash(strings.TrimSpace(path))
	if p == "" {
		return ""
	}
	_, after, ok := strings.Cut(p, WorkerBranchPathSegment)
	if !ok {
		return NormalizeRepoRel(path)
	}
	parts := strings.SplitN(after, "/", 3)
	if len(parts) < 2 || parts[1] == "" {
		return "."
	}
	if len(parts) == 2 || parts[2] == "" {
		return "."
	}
	return parts[2]
}

// RewriteWorkerBranchPaths replaces absolute worker-branch prefixes with
// repo-relative form. A path that is exactly a branch root becomes ".".
func RewriteWorkerBranchPaths(s string) string {
	if s == "" || !strings.Contains(filepath.ToSlash(s), WorkerBranchPathSegment) {
		return s
	}
	out := s
	for range 32 {
		slash := filepath.ToSlash(out)
		idx := strings.Index(slash, WorkerBranchPathSegment)
		if idx < 0 {
			return out
		}
		start := workerBranchTokenStart(slash, idx)
		end, replacement := workerBranchTokenRewrite(slash, start, idx)
		if end <= start || replacement == "" {
			return out
		}
		out = out[:start] + replacement + out[end:]
	}
	return out
}

func workerBranchTokenStart(slash string, segmentIdx int) int {
	start := segmentIdx
	for start > 0 {
		c := slash[start-1]
		if isWorkerBranchTokenBoundary(c) {
			break
		}
		start--
	}
	return start
}

func workerBranchTokenRewrite(slash string, start, segmentIdx int) (end int, replacement string) {
	after := slash[segmentIdx+len(WorkerBranchPathSegment):]
	keySeg := workerBranchSegLen(after)
	keyLen := strings.IndexByte(after, '/')
	if keyLen < 0 || keyLen > keySeg {
		return workerBranchTokenEnd(slash, start), "."
	}
	rest := after[keyLen+1:]
	jobLen := workerBranchSegLen(rest)
	if i := strings.IndexByte(rest, '/'); i >= 0 && i < jobLen {
		jobLen = i
	}
	prefixEnd := segmentIdx + len(WorkerBranchPathSegment) + keyLen + 1 + jobLen
	if prefixEnd < len(slash) && slash[prefixEnd] == '/' {
		end = workerBranchTokenEnd(slash, prefixEnd+1)
		rel := slash[prefixEnd+1 : end]
		if rel == "" {
			return end, "."
		}
		return end, rel
	}
	return workerBranchTokenEnd(slash, prefixEnd), "."
}

func workerBranchTokenEnd(slash string, from int) int {
	end := from
	for end < len(slash) && !isWorkerBranchTokenBoundary(slash[end]) {
		end++
	}
	return end
}

// workerBranchSegLen is the project-key or job-id run up to a token boundary.
func workerBranchSegLen(s string) int {
	for i := 0; i < len(s); i++ {
		if isWorkerBranchTokenBoundary(s[i]) {
			return i
		}
	}
	return len(s)
}

func isWorkerBranchTokenBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '(', ')', '[', ']', '{', '}', '<', '>', '=', ',', ';':
		return true
	default:
		return false
	}
}
