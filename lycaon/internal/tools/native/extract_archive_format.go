package native

import (
	"archive/tar"
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type extractEntryResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// ExtractResult is the extraction receipt: the committed entries, and the
// existing destination files an extraction without overwrite left in place.
type ExtractResult struct {
	Path      string               `json:"path"`
	Dest      string               `json:"dest"`
	Extracted []extractEntryResult `json:"extracted"`
	Entries   int                  `json:"entries"`
	Bytes     int64                `json:"bytes"`
	Skipped   []string             `json:"skipped,omitempty"`
}

// extractBudget meters one extraction. Its ceilings are the catalog's
// archive_extract_entries and archive_extract_bytes, the latter further bound
// by archive_expansion_ratio times the archive's compressed size.
type extractBudget struct {
	entries    int
	bytes      int64
	out        []extractEntryResult
	skipped    []string
	maxEntries int
	maxBytes   int64
}

// archiveLimit reads one archive ceiling from the frozen catalog.
func archiveLimit(id string) int64 {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return 0
	}
	for _, limit := range catalog.Language.Limits {
		if limit.ID == id {
			return limit.Maximum
		}
	}
	return 0
}

func newExtractBudget(archiveBytes int64) (*extractBudget, error) {
	entries, bytes, ratio := archiveLimit("archive_extract_entries"), archiveLimit("archive_extract_bytes"), archiveLimit("archive_expansion_ratio")
	if entries < 1 || bytes < 1 || ratio < 1 {
		return nil, fmt.Errorf("compiled language is missing archive extraction ceilings")
	}
	if archiveBytes > 0 && archiveBytes <= bytes/ratio {
		bytes = archiveBytes * ratio
	}
	return &extractBudget{out: make([]extractEntryResult, 0, 16), maxEntries: int(entries), maxBytes: bytes}, nil
}

func (b *extractBudget) entryLimit() int {
	if b.maxEntries == 0 {
		b.maxEntries = int(archiveLimit("archive_extract_entries"))
	}
	return b.maxEntries
}

func (b *extractBudget) byteLimit() int64 {
	if b.maxBytes == 0 {
		b.maxBytes = archiveLimit("archive_extract_bytes")
	}
	return b.maxBytes
}

func (b *extractBudget) checkEntry() error {
	if b.entries >= b.entryLimit() {
		return &toolrejection.ToolReject{Code: "EXTRACT_ENTRY_LIMIT", Data: map[string]any{
			"max_entries": b.entryLimit(), "entries": b.entries,
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
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz", nil
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return "tar.bz2", nil
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		return "tar.xz", nil
	case strings.HasSuffix(lower, ".tar.zst"), strings.HasSuffix(lower, ".tzst"):
		return "tar.zst", nil
	case strings.HasSuffix(lower, ".tar"):
		return "tar", nil
	case strings.HasSuffix(lower, ".zip"):
		return "zip", nil
	default:
		return "", &toolrejection.ToolReject{
			Code: "EXTRACT_FORMAT_UNSUPPORTED",
			Data: map[string]any{
				"path":    filepath.ToSlash(path),
				"allowed": []string{".zip", ".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".tar.zst", ".tzst"},
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
	return &toolrejection.ToolReject{
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
		mode := f.FileInfo().Mode()
		err := sink.entry(f.Name, mode.IsDir() || strings.HasSuffix(f.Name, "/"), mode&os.ModeSymlink != 0, budget, guard, func() (io.ReadCloser, error) { return f.Open() })
		if err != nil {
			return err
		}
	}
	return nil
}

// extractTarFile extracts a plain, gzip, bzip2, xz or zstd tar stream.
func extractTarFile(sink extractSink, archive fseffect.Location, format string, budget *extractBudget, guard entryGuard) error {
	f, err := fseffect.OpenRead(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stream, closeStream, err := OpenArchiveStream(format, f, info.Size())
	if err != nil {
		return err
	}
	defer closeStream()
	tr := tar.NewReader(stream)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeReg:
		case tar.TypeSymlink, tar.TypeLink:
			if err := sink.entry(hdr.Name, false, true, budget, guard, nil); err != nil {
				return err
			}
			continue
		default:
			continue
		}
		if err := sink.entry(hdr.Name, hdr.Typeflag == tar.TypeDir, false, budget, guard, func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }); err != nil {
			return err
		}
	}
	return nil
}

// extractSink is one extraction's destination and entry selection.
type extractSink struct {
	ctx       context.Context
	tctx      tools.ToolContext
	dest      mutationTarget
	strip     int
	overwrite bool
	selected  func(string) bool
	flatten   bool
}

func (s extractSink) target(rel string) mutationTarget {
	rel = filepath.FromSlash(rel)
	return mutationTarget{
		Abs:      filepath.Join(s.dest.Abs, rel),
		Location: fseffect.Location{Root: s.dest.Location.Root, Rel: filepath.Join(s.dest.Location.Rel, rel)},
	}
}

// entry admits and writes one archive entry. Names are confined before
// selection and stripping, links are refused rather than created, and every
// write passes the entry and byte budgets and the governed-path guard first.
// Flattening writes each file under its base name and skips directories. A
// directory entry naming the archive root (./) is the destination itself.
func (s extractSink) entry(name string, isDir, isLink bool, budget *extractBudget, guard entryGuard, open func() (io.ReadCloser, error)) error {
	if isDir && path.Clean(strings.ReplaceAll(name, "\\", "/")) == "." {
		return nil
	}
	rel, err := safeArchiveEntryName(name)
	if err != nil {
		return err
	}
	if s.selected != nil && !s.selected(rel) {
		return nil
	}
	if s.strip > 0 {
		parts := strings.Split(rel, "/")
		if len(parts) <= s.strip {
			return nil
		}
		rel = strings.Join(parts[s.strip:], "/")
	}
	if s.flatten {
		if isDir {
			return nil
		}
		rel = path.Base(rel)
	}
	target := s.target(rel)
	if !pathWithinDir(s.dest.Abs, target.Abs) {
		return extractZipSlip(name)
	}
	if isLink {
		return &toolrejection.ToolReject{Code: "EXTRACT_ZIP_SLIP", Data: map[string]any{"entry": name, "reason": "archive links are not extracted"}}
	}
	if err := budget.checkEntry(); err != nil {
		return err
	}
	if err := guard(rel, target.Abs); err != nil {
		return err
	}
	if isDir {
		if _, err := mkdirAgentPath(s.ctx, s.tctx, target, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", rel, err)
		}
		budget.addEntry(rel, 0)
		return nil
	}
	if !s.overwrite {
		if _, err := os.Lstat(target.Abs); err == nil {
			budget.skipped = append(budget.skipped, rel)
			return nil
		}
	}
	if _, err := mkdirAgentPath(s.ctx, s.tctx, s.target(filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))), 0o755); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", rel, err)
	}
	rc, err := open()
	if err != nil {
		return fmt.Errorf("open archive entry %s: %w", rel, err)
	}
	n, err := writeExtractFile(s, target, rc, budget)
	_ = rc.Close()
	if err != nil {
		return err
	}
	budget.addEntry(rel, n)
	return nil
}

func writeExtractFile(sink extractSink, target mutationTarget, r io.Reader, budget *extractBudget) (int64, error) {
	remaining := budget.byteLimit() - budget.bytes
	result, err := applyAgentStream(sink.ctx, sink.tctx, agentStreamRequest{
		Target: target, Source: io.LimitReader(r, remaining+1),
		BeforeCommit: func(_ fseffect.Target, staged fseffect.Result) error {
			if staged.Bytes <= remaining {
				return nil
			}
			return &toolrejection.ToolReject{
				Code: "EXTRACT_SIZE_EXCEEDED",
				Data: map[string]any{
					"max_uncompressed_bytes": budget.byteLimit(),
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
