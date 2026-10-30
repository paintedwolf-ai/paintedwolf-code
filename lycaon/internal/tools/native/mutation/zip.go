package mutation

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/native"
)

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
