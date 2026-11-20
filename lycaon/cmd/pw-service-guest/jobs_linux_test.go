package main

import (
	"github.com/lycaon/lycaon/internal/testutil"

	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/serviceguest"
)

func TestGuestRecordsBoundOutputAndWait(t *testing.T) {
	s := newJobs(configuration{})
	j := &job{id: "first", done: make(chan struct{})}
	s.records[j.id] = j
	large := bytes.Repeat([]byte{'x'}, serviceguest.MaxOutput+17)
	if n, err := j.stdout.Write(large); n != len(large) || err != nil {
		t.Fatal(n, err)
	}
	if _, err := j.stderr.Write([]byte("error")); err != nil {
		testutil.FailErr(t, "write job stderr", err)
	}
	selected, err := s.Status(t.Context(), j.id)
	if err != nil || selected.State != serviceguest.Running || !selected.StdoutTruncated || selected.StderrTruncated || string(selected.Stderr) != "error" || len(selected.Stdout) != serviceguest.MaxOutput {
		t.Fatalf("unexpected status: state=%s stdout=%d err=%v", selected.State, len(selected.Stdout), err)
	}
	selected.Stdout[0] = 0
	close(j.done)
	finished, err := s.Wait(t.Context(), j.id)
	if err != nil || finished.State != serviceguest.Exited || finished.Stdout[0] != 'x' {
		t.Fatal(finished.State, err)
	}
	if _, err := s.Stop(t.Context(), "missing"); err == nil {
		t.Fatal("unknown job accepted")
	}
	testutil.FailErr(t, "close job supervisor", s.Close())
	if _, err := s.Start(t.Context(), "next", []string{"unused"}, nil); err == nil {
		t.Fatal("closed executor accepted work")
	}
}

func TestGuestLimitsConcurrentLaunches(t *testing.T) {
	s := newJobs(configuration{})
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7", "8"} {
		s.records[id] = &job{id: id, done: make(chan struct{})}
	}
	if _, err := s.Start(t.Context(), "9", []string{"unused"}, nil); err == nil {
		t.Fatal("ninth active job admitted")
	}
	if _, err := s.Start(t.Context(), "1", []string{"unused"}, nil); err == nil {
		t.Fatal("duplicate id admitted")
	}
}

func TestJobGroupOwnsCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task")
	testutil.FailErr(t, "create job group", os.Mkdir(path, 0700))
	file, err := os.Open(path)
	testutil.FailErr(t, "open job group", err)
	j := &job{group: file}
	if err := j.killGroup(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing cgroup kill control: got %v", err)
	}
	j.releaseGroup()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed job group: got %v", err)
	}
	testutil.FailErr(t, "stop released group", j.killGroup())
}
