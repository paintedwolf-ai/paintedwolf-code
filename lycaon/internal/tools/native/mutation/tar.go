package mutation

import (
	"archive/tar"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	dsbzip2 "github.com/dsnet/compress/bzip2"
	"github.com/lycaon/lycaon/internal/tools/native"
)

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
	var format string
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
	// A named directory selects the entries beneath it, as GNU tar does; the
	// extract owner matches exact entry names.
	var names, members []string
	if inv.flag("-v") || len(inv.operands()) > 0 {
		if names, err = inv.tarNames(archive, strip); err != nil {
			return err
		}
	}
	if len(inv.operands()) > 0 {
		members = make([]string, 0, len(names))
		for _, name := range names {
			members = append(members, path.Clean(strings.TrimPrefix(name, "./")))
		}
		if len(members) == 0 {
			inv.fail(inv.operands()[0], "Not found in archive")
			return nil
		}
	}
	result, err := inv.owners.Operations.Extract.Extract(inv.ctx, native.ExtractInput{Archive: source.Resolved.Abs, Dest: dest, Format: format, StripComponents: strip, Members: members, Overwrite: true}, inv.tc)
	inv.effects += result.Entries
	if err == nil {
		for _, name := range names {
			inv.verbose("%s", name)
		}
	}
	return err
}

// tarNames lists the member names tar -xv reports: the archive's own names
// of the selected entries that extraction keeps.
func (inv *invocation) tarNames(archive string, strip int) ([]string, error) {
	tr, err := inv.tarReader(archive)
	if err != nil {
		return nil, err
	}
	var names []string
	maxEntries := inv.owners.limit("archive_extract_entries")
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		if int64(len(names)) >= maxEntries {
			return nil, limitRefusal("archive_extract_entries", "entries", maxEntries, int64(len(names))+1)
		}
		if _, ok := stripped(hdr.Name, strip); ok && selectedMember(hdr.Name, inv.operands()) {
			names = append(names, hdr.Name)
		}
	}
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
	width := 19
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
		name, ok := stripped(hdr.Name, strip)
		if !ok || !selectedMember(hdr.Name, inv.operands()) {
			continue
		}
		line := name
		if inv.flag("-v") {
			line, width = tarLongListing(hdr, name, width)
		}
		if _, err := io.WriteString(inv.stdout, line+"\n"); err != nil {
			return err
		}
	}
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
		member = strings.TrimSuffix(strings.TrimPrefix(member, "./"), "/")
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
	if err := sink.Close(); err != nil {
		return err
	}
	_, err = inv.publishCreated(archive, out.Bytes(), false)
	return err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
