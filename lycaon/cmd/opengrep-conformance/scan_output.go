package main

import (
	"fmt"
	"io"
	"os"

	scanexec "github.com/lycaon/lycaon/internal/exec"
)

const diagnosticExcerptBytes = 2000

type diagnosticCapture struct {
	head  []byte
	tail  []byte
	total int64
}

func (d *diagnosticCapture) Write(p []byte) (int, error) {
	n := len(p)
	d.total += int64(n)
	if remaining := diagnosticExcerptBytes - len(d.head); remaining > 0 {
		take := min(remaining, len(p))
		d.head = append(d.head, p[:take]...)
		p = p[take:]
	}
	if len(p) >= diagnosticExcerptBytes {
		d.tail = append(d.tail[:0], p[len(p)-diagnosticExcerptBytes:]...)
	} else {
		if excess := len(d.tail) + len(p) - diagnosticExcerptBytes; excess > 0 {
			copy(d.tail, d.tail[excess:])
			d.tail = d.tail[:len(d.tail)-excess]
		}
		d.tail = append(d.tail, p...)
	}
	return n, nil
}

func (d *diagnosticCapture) String() string {
	if d.total <= int64(len(d.head)+len(d.tail)) {
		return string(d.head) + string(d.tail)
	}
	return string(d.head) + "\n[diagnostic output truncated]\n" + string(d.tail)
}

func readEvaluationReport(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- scanner output inside the private evaluation directory.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("scanner report is not a regular file")
	}
	limit := int64(scanexec.DefaultMaxScanOutputBytes)
	if info.Size() > limit {
		return nil, fmt.Errorf("scanner report exceeds %d byte limit", limit)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("scanner report exceeds %d byte limit", limit)
	}
	return raw, nil
}
