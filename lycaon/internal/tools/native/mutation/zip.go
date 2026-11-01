package mutation

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/native"
)

// exclusions reads the patterns of every -x list.
func (inv *invocation) exclusions() (memberPatterns, error) {
	var words []string
	for _, option := range inv.argv.Options("-x") {
		words = append(words, option.Values.Values()...)
	}
	return inv.memberPatterns("-x", words)
}

// runZip creates ARCHIVE from the PATH operands. -x patterns leave out every
// entry whose stored name they match, and -j stores files under their base
// names without directory entries.
func runZip(inv *invocation) error {
	operands := inv.operands()
	if len(operands) < 2 {
		return inv.usage(inv.name(), "ARCHIVE PATH ...")
	}
	excludes, err := inv.exclusions()
	if err != nil {
		return err
	}
	self, err := inv.bind(operands[0], true)
	if err != nil {
		return err
	}
	rule := collectRule{recursive: inv.flag("-r"), exclude: excludes.matchName, archive: self.Resolved.Abs, flatten: inv.flag("-j")}
	entries, err := inv.collect("", operands[1:], rule, inv.createBudget())
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

// unzipSelection admits members that a MEMBER operand matches (all when
// there are none) and no -x pattern matches. Both are wildcards over the
// whole stored name.
type unzipSelection struct {
	members, excludes memberPatterns
}

func (s unzipSelection) selected(name string) bool {
	return (len(s.members) == 0 || s.members.matchName(name)) && !s.excludes.matchName(name)
}

// runUnzip extracts, lists (-l) or pipes (-p) the selected members. Without
// -o an existing file is kept and reported; -n keeps it silently; -j writes
// each file under its base name.
func runUnzip(inv *invocation) error {
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.usage(inv.name(), "ARCHIVE [MEMBER ...]")
	}
	if inv.flag("-o") && inv.flag("-n") {
		return inv.usage("-n", "one of -o and -n")
	}
	members, err := inv.memberPatterns("MEMBER", operands[1:])
	if err != nil {
		return err
	}
	excludes, err := inv.exclusions()
	if err != nil {
		return err
	}
	selection := unzipSelection{members: members, excludes: excludes}
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
			return inv.unzipList(zr, selection)
		}
		return inv.unzipPipe(zr, selection, inv.decodeLimit(int64(len(data))))
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
	result, err := inv.owners.Operations.Extract.Extract(inv.ctx, native.ExtractInput{Archive: source.Resolved.Abs, Dest: dest, Format: "zip", Select: selection.selected, Flatten: inv.flag("-j"), Overwrite: inv.flag("-o")}, inv.tc)
	inv.effects += result.Entries
	if !inv.flag("-q") {
		for _, entry := range result.Extracted {
			_, _ = fmt.Fprintf(inv.stdout, "  inflating: %s\n", entry.Path)
		}
	}
	if !inv.flag("-n") {
		for _, skipped := range result.Skipped {
			inv.fail(skipped, "already exists; not replaced without -o")
		}
	}
	return err
}

func (inv *invocation) unzipList(zr *zip.Reader, selection unzipSelection) error {
	var total uint64
	if _, err := io.WriteString(inv.stdout, "  Length      Date    Time    Name\n---------  ---------- -----   ----\n"); err != nil {
		return err
	}
	files := 0
	for _, f := range zr.File {
		if !selection.selected(f.Name) {
			continue
		}
		files++
		total += f.UncompressedSize64
		if _, err := fmt.Fprintf(inv.stdout, "%9d  %s   %s\n", f.UncompressedSize64, f.Modified.UTC().Format("2006-01-02 15:04"), f.Name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(inv.stdout, "---------                     -------\n%9d                     %d files\n", total, files)
	return err
}

func (inv *invocation) unzipPipe(zr *zip.Reader, selection unzipSelection, maximum int64) error {
	reader := &boundedReader{maximum: maximum}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") || !selection.selected(f.Name) {
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
