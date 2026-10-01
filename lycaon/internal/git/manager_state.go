package git

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitargv"
	"github.com/lycaon/lycaon/internal/gitexec"
)

// GitRefLogEntry pairs a HEAD destination with its reflog subject.
type GitRefLogEntry struct {
	Commit  string `json:"commit"`
	Subject string `json:"subject"`
}

// DefaultRefLogLimit bounds the evidence read for a ref movement.
const DefaultRefLogLimit = 64

// RefLogHead returns newest entries first; missing reflogs return an error.
func (m *Manager) RefLogHead(ctx context.Context, projectDir string, limit int) ([]GitRefLogEntry, error) {
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > DefaultRefLogLimit {
		limit = DefaultRefLogLimit
	}
	args := []string{"reflog", "show", "--format=%H%x1f%gs", fmt.Sprintf("-%d", limit)}
	out, code, err := gitexec.Run(ctx, dir, args, hermeticOpts(0))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git reflog failed: %s", strings.TrimSpace(string(out)))
	}
	var entries []GitRefLogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		commit, subject, ok := strings.Cut(line, "\x1f")
		if !ok || strings.TrimSpace(commit) == "" {
			continue
		}
		entries = append(entries, GitRefLogEntry{
			Commit: strings.TrimSpace(commit), Subject: strings.TrimSpace(subject),
		})
	}
	return entries, nil
}

// TreeOIDs uses projectDir-relative paths and omits missing or non-blob entries.
func (m *Manager) TreeOIDs(ctx context.Context, projectDir, ref string, paths []string) (map[string]string, error) {
	dir, err := absProjectDir(projectDir)
	if err != nil {
		return nil, err
	}
	if ref = strings.TrimSpace(ref); ref == "" {
		ref = "HEAD"
	}
	if err := validateGitRef(ref, "ref"); err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimPrefix(filepath.ToSlash(path), "/")
		if path != "" {
			normalized = append(normalized, path)
		}
	}
	if len(normalized) == 0 {
		return map[string]string{}, nil
	}
	sort.Strings(normalized)
	if ref == "HEAD" {
		if head, headErr := ReadHeadSHA(dir); headErr == nil && FullObjectID(head) {
			ref = head
		}
	}
	if !FullObjectID(ref) {
		out, code, err := gitexec.Run(ctx, dir, []string{"--no-replace-objects", "rev-parse", "--verify", gitargv.EndOfOptions, ref + "^{tree}"}, hermeticOpts(0))
		if err != nil {
			return nil, err
		}
		ref = strings.TrimSpace(string(out))
		if code != 0 || !FullObjectID(ref) {
			return nil, fmt.Errorf("ref did not resolve to one Git tree")
		}
	}
	key := dir + "\x00" + ref + "\x00" + strings.Join(normalized, "\x00")
	value, _, err := m.trees.load(ctx, key, func(ctx context.Context) (map[string]string, int, error) {
		value, err := readTreeOIDs(ctx, dir, ref, normalized)
		size := len(key)
		for path, oid := range value {
			size += len(path) + len(oid) + 64
		}
		return value, size, err
	})
	return maps.Clone(value), err
}

func readTreeOIDs(ctx context.Context, dir, ref string, paths []string) (map[string]string, error) {
	args := []string{"--no-replace-objects", "ls-tree", "-r", "-z", ref, "--"}
	for _, path := range paths {
		args = append(args, ":(literal)"+path)
	}
	requested := len(paths)
	oids := make(map[string]string, requested)
	err := gitexec.RunRecords(ctx, dir, args, hermeticOpts(0), func(raw []byte) error {
		meta, path, ok := strings.Cut(string(raw), "\t")
		if !ok {
			return fmt.Errorf("malformed git tree record")
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return fmt.Errorf("malformed git tree metadata")
		}
		if fields[1] == "blob" {
			oids[path] = fields[2]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return oids, nil
}
