package mutation

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	dsbzip2 "github.com/dsnet/compress/bzip2"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// Archive utilities read through the invocation's read authority, extract
// through the shared extraction operation (confinement, link and governed-path
// guards, catalog entry/byte/expansion ceilings, recoverable writes) and
// publish created archives byte for byte through the pinned write door.

// archiveBudget bounds one creation or decompression by catalog ceilings.
type archiveBudget struct {
	entries, maxEntries int64
	bytes, maxBytes     int64
	limitEntries        string
	limitBytes          string
}

func (b *archiveBudget) entry() error {
	if b.entries++; b.entries > b.maxEntries {
		return limitRefusal(b.limitEntries, "entries", b.maxEntries, b.entries)
	}
	return nil
}

func (b *archiveBudget) add(n int64) error {
	if b.bytes += n; b.bytes > b.maxBytes {
		return limitRefusal(b.limitBytes, "bytes", b.maxBytes, b.bytes)
	}
	return nil
}

func limitRefusal(id, unit string, maximum, observed int64) error {
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": id, "resource_unit": unit, "maximum": maximum, "observed": observed}}
}

func (inv *invocation) createBudget() *archiveBudget {
	return &archiveBudget{maxEntries: inv.owners.limit("archive_create_entries"), maxBytes: inv.owners.limit("archive_create_input_bytes"),
		limitEntries: "archive_create_entries", limitBytes: "archive_create_input_bytes"}
}

// decodeLimit is the most bytes a compressed input of size n may expand to.
func (inv *invocation) decodeLimit(n int64) int64 {
	maximum := inv.owners.limit("archive_extract_bytes")
	if ratio := inv.owners.limit("archive_expansion_ratio"); n > 0 && n <= maximum/ratio {
		maximum = n * ratio
	}
	return maximum
}

// readBound reads one operand's bytes under read authority, refusing more
// than maximum without retaining them.
func (inv *invocation) readBound(operand string, maximum int64, limit string) ([]byte, os.FileInfo, error) {
	bound, err := inv.bind(operand, false)
	if err != nil {
		return nil, nil, err
	}
	f, err := bound.Open()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maximum {
		return nil, nil, limitRefusal(limit, "bytes", maximum, int64(len(data)))
	}

	return data, info, nil
}

// publishCreated captures an output path and publishes created bytes there.
func (inv *invocation) publishCreated(operand string, data []byte, noReplace bool) (bool, error) {
	bound, err := inv.bind(operand, true)
	if err != nil {
		return false, err
	}
	target, err := inv.capture(bound.Resolved.Abs)
	if err != nil {
		return false, err
	}
	if noReplace && target.pin.Mode != toolcontract.AbsentRevision {
		inv.fail(operand, "already exists")
		return false, nil
	}
	if _, err := inv.owners.publish(inv.ctx, inv.tc, target, data); err != nil {
		return false, err
	}
	inv.effects++
	return true, nil
}

// sourceEntry is one file or directory an archive is created from.
type sourceEntry struct {
	name string
	dir  bool
	data []byte
	mode os.FileMode
}

// collect walks operands under base in sorted byte order. Directories are
// descended only when recursive; links are refused, never followed.
func (inv *invocation) collect(base string, operands []string, recursive bool, budget *archiveBudget) ([]sourceEntry, error) {
	var out []sourceEntry
	var visit func(name string) error
	visit = func(name string) error {
		if err := inv.ctx.Err(); err != nil {
			return err
		}
		operand := name
		if base != "" && !filepath.IsAbs(name) {
			operand = filepath.Join(base, name)
		}
		bound, err := inv.bind(operand, false)
		if err != nil {
			return err
		}
		info, err := os.Lstat(bound.Resolved.Abs)
		if err != nil {
			return err
		}
		if err := budget.entry(); err != nil {
			return err
		}
		entryName := filepath.ToSlash(filepath.Clean(name))
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return inv.unsupportedForm(name, "regular files and directories; archive links are not created")
		case info.IsDir():
			out = append(out, sourceEntry{name: strings.TrimSuffix(entryName, "/") + "/", dir: true, mode: info.Mode().Perm()})
			if !recursive {
				return nil
			}
			children, err := os.ReadDir(bound.Resolved.Abs)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(children))
			for _, child := range children {
				names = append(names, child.Name())
			}
			sort.Strings(names)
			for _, child := range names {
				if err := visit(filepath.Join(name, child)); err != nil {
					return err
				}
			}
			return nil
		}
		data, _, err := inv.readBound(operand, budget.maxBytes-budget.bytes, budget.limitBytes)
		if err != nil {
			return err
		}
		if err := budget.add(int64(len(data))); err != nil {
			return err
		}
		out = append(out, sourceEntry{name: entryName, data: data, mode: info.Mode().Perm()})
		return nil
	}
	for _, operand := range operands {
		if err := visit(operand); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// directory resolves -C/-d or the invocation cwd.
func (inv *invocation) directory(key string) (string, error) {
	value, ok := inv.value(key)
	if !ok {
		return inv.paths.Cwd().Abs, nil
	}
	bound, err := inv.bind(value, true)
	if err != nil {
		return "", err
	}
	return bound.Resolved.Abs, nil
}

// sniffFormat detects a tar stream's compression from its magic bytes.
func sniffFormat(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "tar.gz"
	case bytes.HasPrefix(head, []byte("BZh")):
		return "tar.bz2"
	}
	return "tar"
}

func runTar(inv *invocation) error {
	modes := 0
	for _, key := range []string{"-x", "-t", "-c"} {
		if inv.flag(key) {
			modes++
		}
	}
	if modes != 1 {
		return inv.usage(inv.name(), "exactly one of -x, -t or -c")
	}
	if inv.flag("-z") && inv.flag("-j") {
		return inv.usage("-j", "one compression")
	}
	archive, ok := inv.value("-f")
	if !ok || archive == "-" {
		return inv.usage(inv.name(), "-f ARCHIVE naming a file")
	}
	strip := 0
	if value, ok := inv.value("--strip-components"); ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return inv.usage(value, "a nonnegative component count")
		}
		strip = n
	}
	switch {
	case inv.flag("-c"):
		return inv.tarCreate(archive)
	case inv.flag("-t"):
		return inv.tarList(archive, strip)
	}
	format := "tar"
	switch {
	case inv.flag("-z"):
		format = "tar.gz"
	case inv.flag("-j"):
		format = "tar.bz2"
	default:
		data, _, err := inv.readBound(archive, inv.owners.limit("archive_input_bytes"), "archive_input_bytes")
		if err != nil {
			return err
		}
		format = sniffFormat(data)
	}
	dest, err := inv.directory("-C")
	if err != nil {
		return err
	}
	source, err := inv.bind(archive, false)
	if err != nil {
		return err
	}
	result, err := inv.owners.Operations.Extract.Extract(inv.ctx, native.ExtractInput{Archive: source.Resolved.Abs, Dest: dest, Format: format, StripComponents: strip, Members: inv.operands(), Overwrite: true}, inv.tc)
	inv.effects += result.Entries
	return err
}

// tarReader opens a tar stream with the selected or sniffed compression,
// bounded by the catalog's expansion ceilings.
func (inv *invocation) tarReader(archive string) (*tar.Reader, error) {
	data, _, err := inv.readBound(archive, inv.owners.limit("archive_input_bytes"), "archive_input_bytes")
	if err != nil {
		return nil, err
	}
	var stream io.Reader = bytes.NewReader(data)
	format := sniffFormat(data)
	if inv.flag("-z") {
		format = "tar.gz"
	} else if inv.flag("-j") {
		format = "tar.bz2"
	}
	switch format {
	case "tar.gz":
		gz, err := gzip.NewReader(stream)
		if err != nil {
			return nil, err
		}
		stream = gz
	case "tar.bz2":
		stream = bzip2.NewReader(stream)
	}
	return tar.NewReader(&boundedReader{r: stream, remaining: inv.decodeLimit(int64(len(data)))}), nil
}

func (inv *invocation) tarList(archive string, strip int) error {
	tr, err := inv.tarReader(archive)
	if err != nil {
		return err
	}
	maxEntries := inv.owners.limit("archive_extract_entries")
	var entries int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entries++; entries > maxEntries {
			return limitRefusal("archive_extract_entries", "entries", maxEntries, entries)
		}
		name := hdr.Name
		if strip > 0 {
			parts := strings.Split(strings.TrimPrefix(name, "./"), "/")
			if len(parts) <= strip {
				continue
			}
			name = strings.Join(parts[strip:], "/")
		}
		if _, err := io.WriteString(inv.stdout, name+"\n"); err != nil {
			return err
		}
	}
}

func (inv *invocation) tarCreate(archive string) error {
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH to archive")
	}
	base, err := inv.directory("-C")
	if err != nil {
		return err
	}
	entries, err := inv.collect(base, inv.operands(), true, inv.createBudget())
	if err != nil {
		return err
	}
	var out bytes.Buffer
	var sink io.WriteCloser = nopWriteCloser{&out}
	switch {
	case inv.flag("-z"):
		sink = gzip.NewWriter(&out)
	case inv.flag("-j"):
		if sink, err = dsbzip2.NewWriter(&out, nil); err != nil {
			return err
		}
	}
	tw := tar.NewWriter(sink)
	for _, entry := range entries {
		hdr := &tar.Header{Name: entry.name, Mode: int64(entry.mode), Size: int64(len(entry.data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if entry.dir {
			hdr.Typeflag, hdr.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(entry.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := sink.Close(); err != nil {
		return err
	}
	_, err = inv.publishCreated(archive, out.Bytes(), false)
	return err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func runZip(inv *invocation) error {
	operands := inv.operands()
	if len(operands) < 2 {
		return inv.usage(inv.name(), "ARCHIVE PATH ...")
	}
	entries, err := inv.collect("", operands[1:], inv.flag("-r"), inv.createBudget())
	if err != nil {
		return err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		if entry.dir {
			header.Method = zip.Store
			header.SetMode(entry.mode | os.ModeDir)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := w.Write(entry.data); err != nil {
			return err
		}
		if !inv.flag("-q") {
			_, _ = fmt.Fprintf(inv.stdout, "  adding: %s\n", entry.name)
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	_, err = inv.publishCreated(operands[0], out.Bytes(), false)
	return err
}

func runUnzip(inv *invocation) error {
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.usage(inv.name(), "ARCHIVE [MEMBER ...]")
	}
	if inv.flag("-l") || inv.flag("-p") {
		data, _, err := inv.readBound(operands[0], inv.owners.limit("archive_input_bytes"), "archive_input_bytes")
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		if inv.flag("-l") {
			return inv.unzipList(zr)
		}
		return inv.unzipPipe(zr, operands[1:], inv.decodeLimit(int64(len(data))))
	}
	dest, err := inv.directory("-d")
	if err != nil {
		return err
	}
	source, err := inv.bind(operands[0], false)
	if err != nil {
		return err
	}
	if !inv.flag("-q") {
		_, _ = fmt.Fprintf(inv.stdout, "Archive:  %s\n", operands[0])
	}
	result, err := inv.owners.Operations.Extract.Extract(inv.ctx, native.ExtractInput{Archive: source.Resolved.Abs, Dest: dest, Format: "zip", Members: operands[1:], Overwrite: inv.flag("-o")}, inv.tc)
	inv.effects += result.Entries
	if !inv.flag("-q") {
		for _, entry := range result.Extracted {
			_, _ = fmt.Fprintf(inv.stdout, "  inflating: %s\n", entry.Path)
		}
	}
	for _, skipped := range result.Skipped {
		inv.fail(skipped, "already exists; not replaced without -o")
	}
	return err
}

func (inv *invocation) unzipList(zr *zip.Reader) error {
	var total uint64
	if _, err := io.WriteString(inv.stdout, "  Length      Date    Time    Name\n---------  ---------- -----   ----\n"); err != nil {
		return err
	}
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if _, err := fmt.Fprintf(inv.stdout, "%9d  %s   %s\n", f.UncompressedSize64, f.Modified.UTC().Format("2006-01-02 15:04"), f.Name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(inv.stdout, "---------                     -------\n%9d                     %d files\n", total, len(zr.File))
	return err
}

func (inv *invocation) unzipPipe(zr *zip.Reader, members []string, maximum int64) error {
	reader := &boundedReader{remaining: maximum}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") || (len(members) > 0 && !containsName(members, f.Name)) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		reader.r = rc
		_, err = io.Copy(inv.stdout, reader)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if path.Clean(n) == path.Clean(name) {
			return true
		}
	}
	return false
}

// boundedReader refuses decoded bytes past the expansion ceiling.
type boundedReader struct {
	r         io.Reader
	remaining int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if b.remaining -= int64(n); b.remaining < 0 {
		return 0, limitRefusal("archive_extract_bytes", "bytes", 0, -b.remaining)
	}
	return n, err
}

// runGzip replaces each PATH with PATH.gz; the original is removed through
// the recoverable delete only after the compressed file is published.
func runGzip(inv *invocation) error {
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH")
	}
	return inv.each(inv.operands(), func(operand string) (int, error) {
		if strings.HasSuffix(operand, ".gz") {
			inv.fail(operand, "already has .gz suffix -- unchanged")
			return 0, nil
		}
		data, info, err := inv.readBound(operand, inv.owners.limit("archive_create_input_bytes"), "archive_create_input_bytes")
		if err != nil {
			return 0, err
		}
		var out bytes.Buffer
		gz := gzip.NewWriter(&out)
		gz.Name, gz.ModTime = filepath.Base(operand), info.ModTime()
		if _, err := gz.Write(data); err != nil {
			return 0, err
		}
		if err := gz.Close(); err != nil {
			return 0, err
		}
		return inv.replaceCompressed(operand, operand+".gz", out.Bytes())
	})
}

func runGunzip(inv *invocation) error {
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH")
	}
	return inv.each(inv.operands(), func(operand string) (int, error) {
		output, ok := strings.CutSuffix(operand, ".gz")
		if !ok || output == "" {
			inv.fail(operand, "unknown suffix -- ignored")
			return 0, nil
		}
		decoded, err := inv.gunzipFile(operand)
		if err != nil {
			return 0, err
		}
		return inv.replaceCompressed(operand, output, decoded)
	})
}

func (inv *invocation) gunzipFile(operand string) ([]byte, error) {
	data, _, err := inv.readBound(operand, inv.owners.limit("archive_input_bytes"), "archive_input_bytes")
	if err != nil {
		return nil, err
	}
	return inv.gunzipBytes(data)
}

func (inv *invocation) gunzipBytes(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()
	return io.ReadAll(&boundedReader{r: gz, remaining: inv.decodeLimit(int64(len(data)))})
}

// replaceCompressed publishes the new file, refusing to replace an existing
// one, then recoverably removes the input.
func (inv *invocation) replaceCompressed(input, output string, data []byte) (int, error) {
	published, err := inv.publishCreated(output, data, true)
	if err != nil || !published {
		return 0, err
	}
	bound, err := inv.bind(input, true)
	if err != nil {
		return 1, err
	}
	result, err := inv.owners.Operations.Delete.Delete(inv.ctx, native.DeleteInput{Paths: []string{bound.Resolved.Abs}, FilesOnly: true}, inv.tc)
	return 1 + len(result.Deleted), err
}

func runZcat(inv *invocation) error {
	if len(inv.operands()) == 0 {
		data, err := io.ReadAll(io.LimitReader(inv.input, inv.owners.limit("archive_input_bytes")+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > inv.owners.limit("archive_input_bytes") {
			return limitRefusal("archive_input_bytes", "bytes", inv.owners.limit("archive_input_bytes"), int64(len(data)))
		}
		decoded, err := inv.gunzipBytes(data)
		if err != nil {
			return err
		}
		_, err = inv.stdout.Write(decoded)
		return err
	}
	return inv.each(inv.operands(), func(operand string) (int, error) {
		decoded, err := inv.gunzipFile(operand)
		if err != nil {
			return 0, err
		}
		_, err = inv.stdout.Write(decoded)
		return 0, err
	})
}
