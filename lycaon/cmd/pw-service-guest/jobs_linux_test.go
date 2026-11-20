package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/testutil"
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

func TestGuestJobGroupKillControl(t *testing.T) {
	group := t.TempDir()
	control := filepath.Join(group, "cgroup.kill")
	testutil.FailErr(t, "create kill control", os.WriteFile(control, nil, 0600))
	file, err := os.Open(group)
	testutil.FailErr(t, "pin job group", err)
	defer file.Close()
	j := job{group: file}
	testutil.FailErr(t, "terminate job group", j.killGroup())
	value, err := os.ReadFile(control)
	testutil.FailErr(t, "read kill control", err)
	if string(value) != "1" {
		t.Fatalf("cgroup.kill = %q", value)
	}
}

func TestGuestLaunchFailureReleasesGroupAndFinishedHistory(t *testing.T) {
	root := t.TempDir()
	s := newJobs(configuration{scratch: root, groups: root, helper: filepath.Join(root, "absent")})
	for i := range 32 {
		id := strconv.Itoa(i)
		j := &job{id: id, done: make(chan struct{})}
		if i > 0 {
			close(j.done)
		}
		s.records[id] = j
		s.order = append(s.order, id)
	}
	if _, err := s.Start(t.Context(), "next", []string{"/bin/missing"}, nil); err == nil {
		t.Fatal("missing helper accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "next")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed launch kept resources: %v", err)
	}
	if s.records["0"] == nil || s.records["1"] != nil || s.records["2"] == nil || s.records["next"] != nil {
		t.Fatal("launch failure lost active work or retained evicted job")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Wait(ctx, "0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait: %v", err)
	}
	if _, err := s.Status(t.Context(), "1"); err == nil {
		t.Fatal("status retained an evicted job")
	}
	if _, err := s.Wait(t.Context(), "1"); err == nil {
		t.Fatal("wait retained an evicted job")
	}
}
