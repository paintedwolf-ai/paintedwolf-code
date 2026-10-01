package governance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// ErrPathOutsideRoot marks a requested path that resolves outside the project root.
var ErrPathOutsideRoot = errors.New("path escapes project boundary")

// ResolvedAgentsMD is one discovered AGENTS.md file under a workspace root.
type ResolvedAgentsMD struct {
	Path    string // repo-relative POSIX path, e.g. "lycaon/AGENTS.md"
	Content string // empty when loaded index-only
}

// ResolveChain returns applicable AGENTS.md files for relPath under absRoot.
// Order is root-first, nearest-last. Missing ancestor files are skipped.
// When absRoot/<overlay>/AGENTS.md exists it is appended last (project overlay).
func ResolveChain(absRoot string, relPath string) ([]ResolvedAgentsMD, error) {
	return ResolveChainLimited(absRoot, relPath, DefaultAgentsMDInjectMaxBodyBytes)
}

// ResolveChainMetadata returns the runtime chain paths without reading bodies.
func ResolveChainMetadata(absRoot string, relPath string) ([]ResolvedAgentsMD, error) {
	chain, err := resolveChainWithinRoot(absRoot, relPath, false, DefaultAgentsMDInjectMaxBodyBytes)
	if err != nil {
		return nil, err
	}
	overlayRel := settingsoverlay.Rel(protectedpath.AgentsMDFileName)
	overlay, err := loadAgentsMDIfPresent(absRoot, overlayRel, false, DefaultAgentsMDInjectMaxBodyBytes)
	if err != nil {
		return nil, err
	}
	if overlay != nil && !chainHasPath(chain, overlay.Path) {
		chain = append(chain, *overlay)
	}
	return chain, nil
}

// ResolveChainLimited returns the chain with bounded body reads.
func ResolveChainLimited(absRoot string, relPath string, maxBodyBytes int) ([]ResolvedAgentsMD, error) {
	maxBodyBytes = normalizeAgentsMDMaxBodyBytes(maxBodyBytes)
	chain, err := resolveChainWithinRoot(absRoot, relPath, true, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	overlayRel := settingsoverlay.Rel(protectedpath.AgentsMDFileName)
	overlay, err := loadAgentsMDIfPresent(absRoot, overlayRel, true, maxBodyBytes)
	if err != nil {
		return nil, err
	}
	if overlay != nil && !chainHasPath(chain, overlay.Path) {
		chain = append(chain, *overlay)
	}
	return chain, nil
}

// ListIndexOmitDir reports directory base names that must not appear in the
// session-start AGENTS.md index. Separate from sandbox.ShouldSkipDir (survey /
// sourcefeed): index omit is not a universal walk prune.
func ListIndexOmitDir(baseName string) bool {
	switch baseName {
	case "testdata", "node_modules":
		return true
	default:
		return false
	}
}

// ListIndex returns metadata for every AGENTS.md under absRoot. The walk skips
// engine/VCS dirs (.git, overlay), dot-prefixed directories, and index-only omits
// (testdata, node_modules). It never loads file bodies. Path-scoped chain
// resolution still applies AGENTS.md under omitted or hidden trees when work
// is under those trees.
func ListIndex(ctx context.Context, absRoot string) ([]ResolvedAgentsMD, error) {
	root, err := canonicalRoot(absRoot)
	if err != nil {
		return nil, err
	}
	var out []ResolvedAgentsMD
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			relSlash, relErr := relSlashFromRoot(root, path)
			if relErr != nil {
				return relErr
			}
			name := d.Name()
			if relSlash != "" && sandbox.ShouldSkipDir(relSlash, name) {
				return filepath.SkipDir
			}
			if relSlash != "" && sandbox.IsHiddenName(name) {
				return filepath.SkipDir
			}
			if relSlash != "" && ListIndexOmitDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != protectedpath.AgentsMDFileName {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relSlash, relErr := relSlashFromRoot(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, ResolvedAgentsMD{Path: relSlash})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveChainWithinRoot collects ancestor AGENTS.md files for relPath under absRoot.
func resolveChainWithinRoot(absRoot string, relPath string, loadContent bool, maxBodyBytes int) ([]ResolvedAgentsMD, error) {
	root, err := canonicalRoot(absRoot)
	if err != nil {
		return nil, err
	}
	relSlash, err := normalizeRelPath(relPath)
	if err != nil {
		return nil, err
	}
	if err := assertWithinRoot(root, relSlash); err != nil {
		return nil, err
	}

	dirRel := chainDirectory(root, relSlash)

	var chain []ResolvedAgentsMD
	rootAgents := protectedpath.AgentsMDFileName
	if resolved, loadErr := loadAgentsMDIfPresent(root, rootAgents, loadContent, maxBodyBytes); loadErr != nil {
		return nil, loadErr
	} else if resolved != nil {
		chain = append(chain, *resolved)
	}

	if dirRel != "" {
		parts := strings.Split(dirRel, "/")
		prefix := ""
		for _, part := range parts {
			if part == "" {
				continue
			}
			if prefix == "" {
				prefix = part
			} else {
				prefix = prefix + "/" + part
			}
			agentsRel := filepath.ToSlash(filepath.Join(prefix, protectedpath.AgentsMDFileName))
			resolved, loadErr := loadAgentsMDIfPresent(root, agentsRel, loadContent, maxBodyBytes)
			if loadErr != nil {
				return nil, loadErr
			}
			if resolved != nil {
				chain = append(chain, *resolved)
			}
		}
	}
	return chain, nil
}

func chainDirectory(root string, relSlash string) string {
	if relSlash == "." {
		return ""
	}
	full := filepath.Join(root, filepath.FromSlash(relSlash))
	if info, err := os.Lstat(full); err == nil && info.IsDir() {
		return relSlash
	}
	dirRel := filepath.ToSlash(filepath.Dir(relSlash))
	if dirRel == "." {
		return ""
	}
	return dirRel
}

func loadAgentsMDIfPresent(absRoot string, relSlash string, loadContent bool, maxBodyBytes int) (*ResolvedAgentsMD, error) {
	absRoot, err := canonicalRoot(absRoot)
	if err != nil {
		return nil, err
	}
	relSlash, err = normalizeRelPath(relSlash)
	if err != nil {
		return nil, err
	}
	if err := assertWithinRoot(absRoot, relSlash); err != nil {
		return nil, err
	}
	full := filepath.Join(absRoot, filepath.FromSlash(relSlash))
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat AGENTS.md %q: %w", relSlash, err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	resolved := ResolvedAgentsMD{Path: relSlash}
	if loadContent {
		content, readErr := readAgentsMDBody(absRoot, relSlash, maxBodyBytes)
		if readErr != nil {
			return nil, readErr
		}
		resolved.Content = content
	}
	return &resolved, nil
}

func readAgentsMDBody(absRoot string, relSlash string, maxBodyBytes int) (string, error) {
	file, err := fseffect.OpenRead(fseffect.Location{Root: absRoot, Rel: relSlash})
	if err != nil {
		return "", fmt.Errorf("open AGENTS.md %q: %w", relSlash, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat AGENTS.md %q: %w", relSlash, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("AGENTS.md %q is not a regular file", relSlash)
	}
	limit := int64(agentsMDReadBoundBytes(maxBodyBytes)) + 1
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", fmt.Errorf("read AGENTS.md %q: %w", relSlash, err)
	}
	return string(data), nil
}

func normalizeAgentsMDMaxBodyBytes(maxBodyBytes int) int {
	if maxBodyBytes <= 0 {
		return DefaultAgentsMDInjectMaxBodyBytes
	}
	return maxBodyBytes
}

// agentsMDReadBoundBytes is the greater of the caller's inject budget and
// AgentsMDReadMaxBytes, so a larger configured budget is still honored.
func agentsMDReadBoundBytes(maxBodyBytes int) int {
	normalized := normalizeAgentsMDMaxBodyBytes(maxBodyBytes)
	if normalized < AgentsMDReadMaxBytes {
		return AgentsMDReadMaxBytes
	}
	return normalized
}

func canonicalRoot(absRoot string) (string, error) {
	absRoot = strings.TrimSpace(absRoot)
	if absRoot == "" {
		return "", fmt.Errorf("project root is required")
	}
	root, err := filepath.Abs(absRoot)
	if err != nil {
		return "", fmt.Errorf("invalid project root: %w", err)
	}
	if canonical := fspath.CanonicalPath(root); canonical != "" {
		root = canonical
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("project root not accessible: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root is not a directory")
	}
	return root, nil
}

func normalizeRelPath(relPath string) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("relative path is required")
	}
	relPath = filepath.ToSlash(filepath.Clean(relPath))
	relPath = strings.TrimPrefix(relPath, "./")
	if relPath == ".." || strings.HasPrefix(relPath, "../") {
		return "", fmt.Errorf("%w: %q", ErrPathOutsideRoot, relPath)
	}
	return relPath, nil
}

func assertWithinRoot(root string, relSlash string) error {
	full := filepath.Join(root, filepath.FromSlash(relSlash))
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return fmt.Errorf("path resolution failed: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("%w: %q", ErrPathOutsideRoot, relSlash)
	}
	checkPath := full
	if info, err := os.Lstat(full); err == nil && !info.IsDir() {
		checkPath = filepath.Dir(full)
	} else if os.IsNotExist(err) {
		checkPath = filepath.Dir(full)
	}
	if evaluated := fspath.CanonicalPath(checkPath); evaluated != "" {
		if checkPath != full {
			evaluated = filepath.Join(evaluated, filepath.Base(full))
		}
		relEval, err := filepath.Rel(root, evaluated)
		if err != nil {
			return fmt.Errorf("symlink escape check failed: %w", err)
		}
		if relEval == ".." || strings.HasPrefix(relEval, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("%w via symlink: %q", ErrPathOutsideRoot, relSlash)
		}
	}
	return nil
}

func relSlashFromRoot(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func chainHasPath(chain []ResolvedAgentsMD, path string) bool {
	for _, item := range chain {
		if item.Path == path {
			return true
		}
	}
	return false
}
