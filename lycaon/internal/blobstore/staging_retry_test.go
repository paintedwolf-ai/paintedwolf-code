package blobstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCreateStagingTempRetriesOnceOnNotExist(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), stagingDir)
	var mkdirCalls, createCalls int
	tmp, err := createStagingTempWith(
		stageDir,
		".blob-*",
		func(path string, perm os.FileMode) error {
			mkdirCalls++
			return os.MkdirAll(path, perm)
		},
		func(dir, pattern string) (*os.File, error) {
			createCalls++
			if createCalls == 1 {
				// Simulate an external actor removing .staging after MkdirAll.
				_ = os.Remove(dir)
				return nil, os.ErrNotExist
			}
			return os.CreateTemp(dir, pattern)
		},
	)
	testutil.FailErr(t, "create staging temp with retry", err)
	_ = tmp.Close()
	if mkdirCalls != 2 || createCalls != 2 {
		t.Fatalf("calls = (mkdir=%d, create=%d), want (2, 2)", mkdirCalls, createCalls)
	}
}

func TestCreateStagingTempDoesNotLoopOnPersistentNotExist(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), stagingDir)
	var mkdirCalls, createCalls int
	_, err := createStagingTempWith(
		stageDir,
		".blob-*",
		func(string, os.FileMode) error {
			mkdirCalls++
			return nil
		},
		func(string, string) (*os.File, error) {
			createCalls++
			return nil, os.ErrNotExist
		},
	)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
	if mkdirCalls != 2 || createCalls != 2 {
		t.Fatalf("calls = (mkdir=%d, create=%d), want (2, 2)", mkdirCalls, createCalls)
	}
}

func TestCreateStagingTempDoesNotRetryOtherErrors(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), stagingDir)
	var mkdirCalls, createCalls int
	_, err := createStagingTempWith(
		stageDir,
		".blob-*",
		func(string, os.FileMode) error {
			mkdirCalls++
			return nil
		},
		func(string, string) (*os.File, error) {
			createCalls++
			return nil, os.ErrPermission
		},
	)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want os.ErrPermission", err)
	}
	if mkdirCalls != 1 || createCalls != 1 {
		t.Fatalf("calls = (mkdir=%d, create=%d), want (1, 1)", mkdirCalls, createCalls)
	}
}

func TestCreateStagingTempReportsRetryMkdirError(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), stagingDir)
	mkdirErr := errors.New("disk full")
	var mkdirCalls, createCalls int
	_, err := createStagingTempWith(
		stageDir,
		".blob-*",
		func(string, os.FileMode) error {
			mkdirCalls++
			if mkdirCalls == 2 {
				return mkdirErr
			}
			return nil
		},
		func(string, string) (*os.File, error) {
			createCalls++
			return nil, os.ErrNotExist
		},
	)
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("err = %v, want %v", err, mkdirErr)
	}
	if mkdirCalls != 2 || createCalls != 1 {
		t.Fatalf("calls = (mkdir=%d, create=%d), want (2, 1)", mkdirCalls, createCalls)
	}
}

func TestStagingDirIsRetainedAcrossWrites(t *testing.T) {
	store := Store{Root: t.TempDir(), Dir: "prompt-attachments"}
	_, err := store.Put("hello.txt", strings.NewReader("hello"), bytebound.Materialization(1<<10))
	testutil.FailErr(t, "put", err)

	stageDir := filepath.Join(store.dataRoot(), stagingDir)
	info, err := os.Stat(stageDir)
	testutil.FailErr(t, "stat staging dir after put", err)
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", stageDir)
	}
	entries, err := os.ReadDir(stageDir)
	testutil.FailErr(t, "read staging dir after put", err)
	if len(entries) != 0 {
		t.Fatalf("staging dir left %d staged files behind", len(entries))
	}
}
