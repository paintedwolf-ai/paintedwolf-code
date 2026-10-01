package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	_ "github.com/lycaon/lycaon/internal/repomap/gtsqueries"
)

// FileTags returns definitions and skip reasons for one file.
func FileTags(ctx context.Context, absRoot, relPath string) (FileTagOutcome, error) {
	if ctx.Err() != nil {
		return FileTagOutcome{}, ctx.Err()
	}
	root := strings.TrimSpace(absRoot)
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if root == "" || rel == "" {
		return FileTagOutcome{}, fmt.Errorf("repomap: root and path required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return FileTagOutcome{}, fmt.Errorf("repomap: abs root: %w", err)
	}
	fullPath := filepath.Join(absRoot, filepath.FromSlash(rel))
	info, err := os.Stat(fullPath)
	if err != nil {
		return FileTagOutcome{}, err
	}
	if info.IsDir() {
		return FileTagOutcome{}, fmt.Errorf("repomap: path is a directory: %s", rel)
	}
	if info.Size() > DefaultWalkMaxFileBytes {
		return FileTagOutcome{Skip: SkipStats{Oversized: 1}}, nil
	}
	fsRoot, err := os.OpenRoot(absRoot)
	if err != nil {
		return FileTagOutcome{}, fmt.Errorf("repomap: open root: %w", err)
	}
	defer func() { _ = fsRoot.Close() }()
	src, err := readFileUnderRoot(fsRoot, absRoot, fullPath)
	if err != nil {
		return FileTagOutcome{}, err
	}
	entry := detectGrammar(ctx, filepath.Base(rel), src, filekind.DepthDeep)
	if entry == nil {
		return FileTagOutcome{Skip: SkipStats{NoGrammar: 1}}, nil
	}
	cache := &taggerCache{}
	return tagSourceWithGrammar(ctx, rel, src, *entry, cache), nil
}

// TagsFromBytes returns bounded definition tags for source bytes.
func TagsFromBytes(ctx context.Context, relHint string, src []byte) FileTagOutcome {
	if len(src) > DefaultWalkMaxFileBytes {
		return FileTagOutcome{Skip: SkipStats{Oversized: 1}}
	}
	entry := detectGrammar(ctx, filepath.Base(strings.TrimSpace(relHint)), src, filekind.DepthDeep)
	if entry == nil {
		return FileTagOutcome{Skip: SkipStats{NoGrammar: 1}}
	}
	return tagSourceWithGrammar(ctx, relHint, src, *entry, &taggerCache{})
}
