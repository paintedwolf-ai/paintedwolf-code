package search

import (
	"os"
	"path/filepath"
	"testing"
)

type trackedReadHandle struct {
	*os.File
	closed *int
}

func (h trackedReadHandle) Close() error {
	*h.closed++
	return h.File.Close()
}

func atomicReplaceFile(t *testing.T, dir, target, content string) {
	t.Helper()
	tmp := filepath.Join(dir, ".swap.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatalf("write swap temp: %v", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatalf("atomic rename: %v", err)
	}
}

func TestReadCodeBytesRetriesAtomicRegularFileReplacementAndClosesDiscardedHandle(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(target, []byte("base_alpha rev_0\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	var attempts, closes int
	raw, status := readCodeBytesWith(
		target,
		codeExecutorMaxFileBytes,
		func(p string) (os.FileInfo, error) {
			info, err := os.Lstat(p)
			if err != nil {
				return nil, err
			}
			attempts++
			if attempts == 1 {
				// Simulate atomic rename(2) replacing the regular file between Lstat and OpenRead.
				atomicReplaceFile(t, dir, target, "base_alpha rev_1\n")
			}
			return info, nil
		},
		func(p string) (codeReadHandle, error) {
			f, err := os.Open(p)
			if err != nil {
				return nil, err
			}
			return trackedReadHandle{File: f, closed: &closes}, nil
		},
	)
	if status != codeOpenOK {
		t.Fatalf("status=%v want=codeOpenOK", status)
	}
	if string(raw) != "base_alpha rev_1\n" {
		t.Fatalf("raw=%q want=%q", string(raw), "base_alpha rev_1\n")
	}
	if attempts != 2 || closes != 2 {
		t.Fatalf("attempts=%d closes=%d, want 2/2 (discarded handle closed before retry)", attempts, closes)
	}
}

func TestReadCodeBytesPersistentAtomicReplacementReportsUnsearchable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(target, []byte("base_alpha rev_0\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	var attempts, closes int
	raw, status := readCodeBytesWith(
		target,
		codeExecutorMaxFileBytes,
		func(p string) (os.FileInfo, error) {
			info, err := os.Lstat(p)
			if err != nil {
				return nil, err
			}
			attempts++
			atomicReplaceFile(t, dir, target, "base_alpha rev_churn\n")
			return info, nil
		},
		func(p string) (codeReadHandle, error) {
			f, err := os.Open(p)
			if err != nil {
				return nil, err
			}
			return trackedReadHandle{File: f, closed: &closes}, nil
		},
	)
	if status != codeOpenUnsearchable || raw != nil {
		t.Fatalf("status=%v raw=%q, want codeOpenUnsearchable/nil", status, string(raw))
	}
	if attempts != codeReadReplaceAttempts || closes != codeReadReplaceAttempts {
		t.Fatalf("attempts=%d closes=%d, want %d/%d", attempts, closes, codeReadReplaceAttempts, codeReadReplaceAttempts)
	}
}

func TestReadCodeBytesGenuineDeletionAndIneligibleLeafRemainMissing(t *testing.T) {
	t.Run("deleted_between_lstat_and_open", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "gone.txt")
		if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		var attempts int
		_, status := readCodeBytesWith(
			target,
			codeExecutorMaxFileBytes,
			func(p string) (os.FileInfo, error) {
				info, err := os.Lstat(p)
				if err != nil {
					return nil, err
				}
				attempts++
				if err := os.Remove(p); err != nil {
					t.Fatalf("remove file: %v", err)
				}
				return info, nil
			},
			openCodeReadFile,
		)
		if status != codeOpenMissing || attempts != 1 {
			t.Fatalf("status=%v attempts=%d, want codeOpenMissing/1", status, attempts)
		}
	})

	t.Run("symlink_leaf_rejected_without_retry", func(t *testing.T) {
		dir := t.TempDir()
		realFile := filepath.Join(dir, "real.txt")
		if err := os.WriteFile(realFile, []byte("base_alpha\n"), 0o600); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		link := filepath.Join(dir, "link.txt")
		if err := os.Symlink(realFile, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		var attempts int
		_, status := readCodeBytesWith(
			link,
			codeExecutorMaxFileBytes,
			func(p string) (os.FileInfo, error) {
				attempts++
				return os.Lstat(p)
			},
			openCodeReadFile,
		)
		if status != codeOpenMissing || attempts != 1 {
			t.Fatalf("status=%v attempts=%d, want codeOpenMissing/1", status, attempts)
		}
	})

	t.Run("replaced_with_directory_between_lstat_and_open", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "slot")
		if err := os.WriteFile(target, []byte("base_alpha\n"), 0o600); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		var attempts, closes int
		_, status := readCodeBytesWith(
			target,
			codeExecutorMaxFileBytes,
			func(p string) (os.FileInfo, error) {
				info, err := os.Lstat(p)
				if err != nil {
					return nil, err
				}
				attempts++
				if err := os.Remove(p); err != nil {
					t.Fatalf("remove: %v", err)
				}
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return info, nil
			},
			func(p string) (codeReadHandle, error) {
				f, err := os.Open(p)
				if err != nil {
					return nil, err
				}
				return trackedReadHandle{File: f, closed: &closes}, nil
			},
		)
		if status != codeOpenMissing || attempts != 1 || closes != 1 {
			t.Fatalf("status=%v attempts=%d closes=%d, want codeOpenMissing/1/1", status, attempts, closes)
		}
	})
}

func TestOpenCodeDocumentPreservesByteLimitAndRegularRead(t *testing.T) {
	dir := t.TempDir()
	okFile := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(okFile, []byte("base_alpha\n"), 0o600); err != nil {
		t.Fatalf("seed ok file: %v", err)
	}
	doc, status := openCodeDocument(okFile, 1024)
	if status != codeOpenOK || doc.Text() != "base_alpha\n" {
		t.Fatalf("openCodeDocument ok: status=%v text=%q", status, doc.Text())
	}
	bigFile := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigFile, make([]byte, 2048), 0o600); err != nil {
		t.Fatalf("seed big file: %v", err)
	}
	_, status = openCodeDocument(bigFile, 1024)
	if status != codeOpenUnsearchable {
		t.Fatalf("openCodeDocument oversize: status=%v want=codeOpenUnsearchable", status)
	}
}
