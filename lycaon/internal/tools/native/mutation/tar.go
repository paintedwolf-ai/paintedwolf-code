package mutation

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	dsbzip2 "github.com/dsnet/compress/bzip2"
	"github.com/ulikunitz/xz"

	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// tarCodecs maps each compression option to its format.
var tarCodecs = []struct{ key, format string }{
	{"-z", "tar.gz"}, {"-j", "tar.bz2"}, {"-J", "tar.xz"}, {"--zstd", "tar.zst"},
}

// tarRun is one tar invocation's selection: member operands and -T names,
// and the --exclude, -X and --exclude-vcs patterns.
type tarRun struct {
	inv      *invocation
	archive  string
	strip    int
	members  []string
	excludes memberPatterns
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
	archive, ok := inv.value("-f")
	if !ok || archive == "-" {
		return inv.usage(inv.name(), "-f ARCHIVE naming a file")
	}
	r := &tarRun{inv: inv, archive: archive}
	if value, ok := inv.value("--strip-components"); ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return inv.usage(value, "a nonnegative component count")
		}
		r.strip = n
	}
	if err := r.selection(); err != nil {
		return err
	}
	switch {
	case inv.flag("-c"):
		return r.create()
	case inv.flag("-t"):
		return r.list()
	}
	return r.extract()
}

// selection reads the member operands, -T lists and exclusion patterns.
func (r *tarRun) selection() error {
	inv := r.inv
	r.members = inv.operands()
	for _, option := range inv.argv.Options("-T") {
		names, err := inv.listFile(option.Values[0].Value)
		if err != nil {
			return err
		}
		r.members = append(r.members, names...)
	}
	var patterns []string
	for _, option := range inv.argv.Options("--exclude") {
		patterns = append(patterns, option.Values[0].Value)
	}
	for _, option := range inv.argv.Options("-X") {
		lines, err := inv.listFile(option.Values[0].Value)
		if err != nil {
			return err
		}
		patterns = append(patterns, lines...)
	}
	excludes, err := inv.memberPatterns("--exclude", patterns)
	if err != nil {
		return err
	}
	if inv.flag("--exclude-vcs") {
		vcs, err := inv.memberPatterns("--exclude-vcs", vcsNames)
		if err != nil {
			return err
		}
		excludes = append(excludes, vcs...)
	}
	r.excludes = excludes
	return nil
}

// codec is the selected compression; with none, extraction and listing
// read the archive's magic bytes and -a creation reads the archive suffix.
func (r *tarRun) codec(head []byte) (string, error) {
	format := ""
	for _, codec := range tarCodecs {
		if r.inv.flag(codec.key) {
			if format != "" {
				return "", r.inv.usage(codec.key, "one compression")
			}
			format = codec.format
		}
	}
	switch {
	case format != "":
		return format, nil
	case r.inv.flag("-c") && r.inv.flag("-a"):
		return autoCompression(r.archive), nil
	case r.inv.flag("-c"):
		return "tar", nil
	}
	return native.SniffArchiveFormat(head), nil
}

// autoCompression chooses the codec -a selects from the archive suffix.
func autoCompression(archive string) string {
	lower := strings.ToLower(archive)
	for _, suffix := range []struct{ suffix, format string }{
		{".tar.gz", "tar.gz"}, {".tgz", "tar.gz"}, {".taz", "tar.gz"},
		{".tar.bz2", "tar.bz2"}, {".tbz", "tar.bz2"}, {".tbz2", "tar.bz2"}, {".tz2", "tar.bz2"},
		{".tar.xz", "tar.xz"}, {".txz", "tar.xz"},
		{".tar.zst", "tar.zst"}, {".tzst", "tar.zst"},
	} {
		if strings.HasSuffix(lower, suffix.suffix) {
			return suffix.format
		}
	}
	return "tar"
}

// selected reports a member the operands name or lie above, that no
// exclusion matches.
func (r *tarRun) selected(name string) bool {
	return selectedMember(name, r.members) && !r.excludes.matchComponents(name)
}

// extract streams the archive through the shared extraction owner. The
// archive is read here only to sniff an unnamed compression or to list the
// names -v and member operands report.
func (r *tarRun) extract() error {
	inv := r.inv
	format, err := r.codec(nil)
	if err != nil {
		return err
	}
	listNames := inv.flag("-v") || len(r.members) > 0
	var data []byte
	if format == "tar" || listNames {
		if data, _, err = inv.readBound(r.archive, inv.owners.limit("archive_input_bytes"), "archive_input_bytes"); err != nil {
			return err
		}
		if format, err = r.codec(data); err != nil {
			return err
		}
	}
	dest, err := inv.directory("-C")
	if err != nil {
		return err
	}
	source, err := inv.bind(r.archive, false)
	if err != nil {
		return err
	}
	var names []string
	if listNames {
		if names, err = r.names(data, format); err != nil {
			return err
		}
	}
	if len(r.members) > 0 && len(names) == 0 {
		inv.fail(r.members[0], "Not found in archive")
		return nil
	}
	result, err := inv.owners.Operations.Extract.Extract(inv.ctx, native.ExtractInput{Archive: source.Resolved.Abs, Dest: dest, Format: format, StripComponents: r.strip, Select: r.selected, Overwrite: true}, inv.tc)
	inv.effects += result.Entries
	if err == nil {
		for _, name := range names {
			inv.verbose("%s", name)
		}
	}
	return err
}

// names lists the member names tar -xv reports: the archive's own names of
// the selected entries that extraction keeps.
func (r *tarRun) names(data []byte, format string) ([]string, error) {
	var names []string
	err := r.each(data, format, func(hdr *tar.Header, _ string) error {
		names = append(names, hdr.Name)
		return nil
	})
	return names, err
}

// each visits every selected entry that survives --strip-components, under
// the catalog's entry and expansion ceilings.
func (r *tarRun) each(data []byte, format string, visit func(*tar.Header, string) error) error {
	inv := r.inv
	stream, closeStream, err := native.OpenArchiveStream(format, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	defer closeStream()
	tr := tar.NewReader(&boundedReader{r: stream, maximum: inv.decodeLimit(int64(len(data)))})
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
		name, ok := stripped(hdr.Name, r.strip)
		if !ok || !r.selected(hdr.Name) {
			continue
		}
		if err := visit(hdr, name); err != nil {
			return err
		}
	}
}

func (r *tarRun) list() error {
	inv := r.inv
	data, _, err := inv.readBound(r.archive, inv.owners.limit("archive_input_bytes"), "archive_input_bytes")
	if err != nil {
		return err
	}
	format, err := r.codec(data)
	if err != nil {
		return err
	}
	width := 19
	return r.each(data, format, func(hdr *tar.Header, name string) error {
		line := name
		if inv.flag("-v") {
			line, width = tarLongListing(hdr, name, width)
		}
		_, err := io.WriteString(inv.stdout, line+"\n")
		return err
	})
}

// stripped removes --strip-components leading components; an entry with no
// components left is skipped, as tar does.
func stripped(name string, strip int) (string, bool) {
	if strip == 0 {
		return name, true
	}
	parts := strings.Split(strings.TrimPrefix(name, "./"), "/")
	if len(parts) <= strip || (len(parts) == strip+1 && parts[strip] == "") {
		return "", false
	}
	return strings.Join(parts[strip:], "/"), true
}

// selectedMember reports whether a member is named, or lies under a named
// directory; no member operands select every entry.
func selectedMember(name string, members []string) bool {
	if len(members) == 0 {
		return true
	}
	clean := strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	for _, member := range members {
		member = strings.TrimSuffix(strings.TrimPrefix(path.Clean(member), "./"), "/")
		if clean == member || strings.HasPrefix(clean, member+"/") {
			return true
		}
	}
	return false
}

// tarLongListing is GNU tar's tv line. The owner/size column widens to the
// longest seen so far, starting at 19, and the time is local.
func tarLongListing(hdr *tar.Header, name string, width int) (string, int) {
	kind := byte('-')
	switch hdr.Typeflag {
	case tar.TypeDir:
		kind = 'd'
	case tar.TypeSymlink:
		kind = 'l'
	case tar.TypeLink:
		kind = 'h'
	case tar.TypeChar:
		kind = 'c'
	case tar.TypeBlock:
		kind = 'b'
	case tar.TypeFifo:
		kind = 'p'
	}
	mode := modeString(kind, os.FileMode(hdr.Mode)&os.ModePerm, hdr.Mode&0o4000 != 0, hdr.Mode&0o2000 != 0, hdr.Mode&0o1000 != 0)
	user, group := hdr.Uname, hdr.Gname
	if user == "" {
		user = strconv.Itoa(hdr.Uid)
	}
	if group == "" {
		group = strconv.Itoa(hdr.Gid)
	}
	size := strconv.FormatInt(hdr.Size, 10)
	if kind == 'c' || kind == 'b' {
		size = fmt.Sprintf("%d,%d", hdr.Devmajor, hdr.Devminor)
	}
	pad := len(user) + 1 + len(group) + 1 + len(size)
	width = max(width, pad)
	line := fmt.Sprintf("%s %s/%s %*s %s %s", mode, user, group, width-pad+len(size), size, hdr.ModTime.Local().Format("2006-01-02 15:04"), name)
	switch kind {
	case 'l':
		line += " -> " + hdr.Linkname
	case 'h':
		line += " link to " + hdr.Linkname
	}
	return line, width
}

// create archives the operands and -T names under -C, leaving out excluded
// entries and everything beneath an excluded directory, and the archive
// itself.
func (r *tarRun) create() error {
	inv := r.inv
	if len(r.members) == 0 {
		return inv.usage(inv.name(), "at least one PATH to archive, or -T FILE")
	}
	format, err := r.codec(nil)
	if err != nil {
		return err
	}
	base, err := inv.directory("-C")
	if err != nil {
		return err
	}
	self, err := inv.bind(r.archive, true)
	if err != nil {
		return err
	}
	rule := collectRule{recursive: true, exclude: r.excludes.matchComponents, prune: true, archive: self.Resolved.Abs}
	entries, err := inv.collect(base, r.members, rule, inv.createBudget())
	if err != nil {
		return err
	}
	var plain bytes.Buffer
	tw := tar.NewWriter(&plain)
	for _, entry := range entries {
		inv.verbose("%s", entry.name)
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
	data, err := compressTar(format, plain.Bytes())
	if err != nil {
		return err
	}
	_, err = inv.publishCreated(r.archive, data, false)
	return err
}

// compressTar encodes a complete tar stream: gzip at its default level, the
// pinned bzip2 writer, xz with an 8 MiB dictionary, or zstd through the
// host codec.
func compressTar(format string, plain []byte) ([]byte, error) {
	var out bytes.Buffer
	var sink io.WriteCloser
	var err error
	switch format {
	case "tar":
		return plain, nil
	case "tar.gz":
		sink = gzip.NewWriter(&out)
	case "tar.bz2":
		sink, err = dsbzip2.NewWriter(&out, nil)
	case "tar.xz":
		sink, err = xz.NewWriter(&out)
	case "tar.zst":
		return zstdcodec.Compress(bytes.NewReader(plain))
	default:
		return nil, fmt.Errorf("unsupported archive format %q", format)
	}
	if err != nil {
		return nil, err
	}
	if _, err := sink.Write(plain); err != nil {
		return nil, err
	}
	if err := sink.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
