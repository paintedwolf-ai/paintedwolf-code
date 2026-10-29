package mutation

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/native"
)

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
