package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNewServeLoggerWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serve.log")
	t.Setenv("LYCAON_LOG_FILE", path)
	t.Setenv("LYCAON_LOG_LEVEL", "info")

	logger := NewServeLogger()
	if got := ActiveLogFilePath(); got != path {
		t.Fatalf("ActiveLogFilePath() = %q want %q", got, path)
	}

	logger.Info("hello file")

	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "hello file") {
		t.Fatalf("log file missing message: %q", data)
	}
}

func TestNewServeLoggerMissingFileContinuesOnStderr(t *testing.T) {
	t.Setenv("LYCAON_LOG_FILE", "")
	if got := ResolveLogFilePath(); got != "" {
		t.Fatalf("ResolveLogFilePath() = %q want empty", got)
	}
	logger := NewServeLogger()
	if got := ActiveLogFilePath(); got != "" {
		t.Fatalf("ActiveLogFilePath() = %q want empty", got)
	}
	logger.Info("stderr only")
}

func TestNewServeLoggerInvalidPathFallsBackToStderr(t *testing.T) {
	t.Setenv("LYCAON_LOG_FILE", "/dev/null/not-a-file/serve.log")
	logger := NewServeLogger()
	if got := ActiveLogFilePath(); got != "" {
		t.Fatalf("ActiveLogFilePath() = %q want empty on open failure", got)
	}
	logger.Warn("still works")
}
