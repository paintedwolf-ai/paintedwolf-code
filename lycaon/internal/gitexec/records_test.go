package gitexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	command "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunRecordsBeyondCaptureBudget(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	names := []string{"first.txt", "with\ttab.txt", "with\nnewline.txt", "z-last.txt"}
	for _, name := range names {
		writeFile(t, dir, name, "body")
	}
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	_, code, err := Run(t.Context(), dir, args, Opts{MaxOutput: 8})
	if code != 0 || !errors.Is(err, command.ErrOutputTruncated) {
		t.Fatalf("bounded capture: code=%d err=%v", code, err)
	}
	var got []string
	testutil.FailErr(t, "stream records", RunRecords(t.Context(), dir, args, Opts{MaxOutput: 8}, func(p []byte) error { got = append(got, string(p)); return nil }))
	if strings.Join(got, "\x00") != strings.Join(names, "\x00") {
		t.Fatalf("paths = %q, want %q", got, names)
	}
}

func TestRecordWriterChunkingAndFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var got []string
	w := recordWriter{ctx: ctx, cancel: cancel, consume: func(p []byte) error { got = append(got, string(p)); return nil }}
	for _, part := range []string{"one\x00tw", "o\x00", "\x00last", "\x00"} {
		_, err := w.Write([]byte(part))
		testutil.FailErr(t, "write chunk", err)
	}
	if strings.Join(got, "|") != "one|two||last" {
		t.Fatalf("records: %q", got)
	}
	_, err := w.Write([]byte(strings.Repeat("x", MaxRecordBytes+1)))
	testutil.FailErr(t, "drain oversized record", err)
	if w.err == nil || ctx.Err() == nil {
		t.Fatal("oversized record did not cancel producer")
	}
}

func TestRunRecordsConsumerFailure(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	writeFile(t, dir, "a", "body")
	stopped := errors.New("consumer stopped")
	err := RunRecords(t.Context(), dir, []string{"ls-files", "-z", "--others"}, Opts{}, func([]byte) error { return stopped })
	if !errors.Is(err, stopped) {
		t.Fatalf("consumer error: %v", err)
	}
	err = RunRecords(t.Context(), dir, []string{"rev-parse", "--show-toplevel"}, Opts{}, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("accepted unterminated record")
	}
}
