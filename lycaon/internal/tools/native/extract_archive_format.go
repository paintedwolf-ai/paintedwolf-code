package native

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/tools"
)

const (
	hostExtractMaxEntries           = 500
	hostExtractMaxUncompressedBytes = 50 << 20 // 50 MiB
)

type extractEntryResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type extractArchiveResponse struct {
	Path      string               `json:"path"`
	Dest      string               `json:"dest"`
	Extracted []extractEntryResult `json:"extracted"`
	Entries   int                  `json:"entries"`
	Bytes     int64                `json:"bytes"`
}

type extractBudget struct {
	entries int
	bytes   int64
	out     []extractEntryResult
}

func (b *extractBudget) checkEntry() error {
	if b.entries >= hostExtractMaxEntries {
		return &tools.ToolReject{Code: "EXTRACT_ENTRY_LIMIT", Data: map[string]any{
			"max_entries": hostExtractMaxEntries, "entries": b.entries,
		}}
	}
	return nil
}

func (b *extractBudget) addEntry(relDest string, n int64) {
	b.entries++
	b.bytes += n
	b.out = append(b.out, extractEntryResult{Path: relDest, Bytes: n})
}

func archiveFormat(path string) (string, error) {
	lower := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		return "tar.gz", nil
	case strings.HasSuffix(lower, ".zip"):
		return "zip", nil
	default:
		return "", &tools.ToolReject{
			Code: "EXTRACT_FORMAT_UNSUPPORTED",
			Data: map[string]any{
				"path":    filepath.ToSlash(path),
				"allowed": []string{".zip", ".tar.gz"},
			},
		}
	}
}

func safeArchiveEntryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" {
		return "", extractZipSlip(name)
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return "", extractZipSlip(name)
	}
	parts := strings.Split(name, "/")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", extractZipSlip(name)
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", extractZipSlip(name)
	}
	return strings.Join(clean, "/"), nil
}

func extractZipSlip(name string) error {
	return &tools.ToolReject{
		Code: "EXTRACT_ZIP_SLIP",
		Data: map[string]any{"entry": name},
	}
}

// entryGuard validates each archive destination before extraction.
type entryGuard func(relEntry, absTarget string) error

func extractZipFile(sink extractSink, archive fseffect.Location, budget *extractBudget, guard entryGuard) error {
	f, err := fseffect.OpenRead(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	for _, f := range zr.File {
		rel, err := safeArchiveEntryName(f.Name)
		if err != nil {
			return err
		}
		target := sink.target(rel)
		if !pathWithinDir(sink.dest.Abs, target.Abs) {
			return extractZipSlip(f.Name)
		}
		if err := budget.checkEntry(); err != nil {
			return err
		}
		if err := guard(rel, target.Abs); err != nil {
			return err
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(rel, "/") {
			if err := mkdirAgentPath(sink.ctx, sink.tctx, target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", rel, err)
			}
			budget.addEntry(rel, 0)
			continue
		}
		if err := mkdirAgentPath(sink.ctx, sink.tctx, sink.target(filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))), 0o755); err != nil {
			return fmt.Errorf("mkdir parent %s: %w", rel, err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", rel, err)
		}
		n, err := writeExtractFile(sink, target, rc, budget)
		_ = rc.Close()
		if err != nil {
			return err
		}
		budget.addEntry(rel, n)
	}
	return nil
}

func extractTarGzFile(sink extractSink, archive fseffect.Location, budget *extractBudget, guard entryGuard) error {
	f, err := fseffect.OpenRead(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		rel, err := safeArchiveEntryName(hdr.Name)
		if err != nil {
			return err
		}
		target := sink.target(rel)
		if !pathWithinDir(sink.dest.Abs, target.Abs) {
			return extractZipSlip(hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeReg:
		default:
			continue
		}
		if err := budget.checkEntry(); err != nil {
			return err
		}
		if err := guard(rel, target.Abs); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeDir || strings.HasSuffix(rel, "/") {
			if err := mkdirAgentPath(sink.ctx, sink.tctx, target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", rel, err)
			}
			budget.addEntry(rel, 0)
			continue
		}
		if err := mkdirAgentPath(sink.ctx, sink.tctx, sink.target(filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))), 0o755); err != nil {
			return fmt.Errorf("mkdir parent %s: %w", rel, err)
		}
		n, err := writeExtractFile(sink, target, tr, budget)
		if err != nil {
			return err
		}
		budget.addEntry(rel, n)
	}
	return nil
}

type extractSink struct {
	ctx  context.Context
	tctx tools.ToolContext
	dest mutationTarget
}

func (s extractSink) target(rel string) mutationTarget {
	rel = filepath.FromSlash(rel)
	return mutationTarget{
		Abs:      filepath.Join(s.dest.Abs, rel),
		Location: fseffect.Location{Root: s.dest.Location.Root, Rel: filepath.Join(s.dest.Location.Rel, rel)},
	}
}

func writeExtractFile(sink extractSink, target mutationTarget, r io.Reader, budget *extractBudget) (int64, error) {
	remaining := hostExtractMaxUncompressedBytes - budget.bytes
	result, err := applyAgentStream(sink.ctx, sink.tctx, agentStreamRequest{
		Target: target, Source: io.LimitReader(r, remaining+1),
		BeforeCommit: func(_ fseffect.Target, staged fseffect.Result) error {
			if staged.Bytes <= remaining {
				return nil
			}
			return &tools.ToolReject{
				Code: "EXTRACT_SIZE_EXCEEDED",
				Data: map[string]any{
					"max_uncompressed_bytes": hostExtractMaxUncompressedBytes,
					"bytes":                  budget.bytes + staged.Bytes,
				},
			}
		},
	})
	if err != nil {
		return 0, err
	}
	return result.Bytes, nil
}

// pathWithinDir rejects existing symlink escapes from base.
func pathWithinDir(base, target string) bool {
	baseAbs, targetAbs := fspath.CanonicalPath(base), fspath.CanonicalPath(target)
	if baseAbs == "" || targetAbs == "" {
		return false
	}
	// Canonical resolution falls back to the deepest reachable ancestor rather
	// than failing, so an unstattable base would be compared as a plain string.
	// A missing target is ordinary — it is what is about to be created.
	if info, err := os.Stat(baseAbs); err != nil || !info.IsDir() {
		return false
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
