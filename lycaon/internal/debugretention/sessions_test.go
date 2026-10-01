package debugretention

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPruneSessionsHonorsCountBytesAndActiveCapture(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(debugpaths.DebugRootUnder(root), "sessions")
	now := time.Now()
	for i, name := range []string{"old", "middle", "active"} {
		path := filepath.Join(sessions, name, "llm-requests.jsonl")
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write", os.WriteFile(path, []byte("12345678"), 0o600))
		stamp := now.Add(time.Duration(i-3) * time.Hour)
		testutil.FailErr(t, "age file", os.Chtimes(path, stamp, stamp))
	}
	t.Setenv("LYCAON_LLM_DEBUG_FILE", filepath.Join(sessions, "active", "llm-requests.jsonl"))

	err := PruneSessions(t.Context(), root, Config{
		MaxSessions: 2, MaxTotalBytes: 16, SweepInterval: time.Hour,
	})
	testutil.FailErr(t, "prune", err)
	if _, err := os.Stat(filepath.Join(sessions, "old")); !os.IsNotExist(err) {
		t.Fatalf("old session survived: %v", err)
	}
	for _, name := range []string{"middle", "active"} {
		if _, err := os.Stat(filepath.Join(sessions, name)); err != nil {
			t.Fatalf("session %s removed: %v", name, err)
		}
	}
}

func TestDefaultConfigIsBounded(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxFileBytes <= 0 || cfg.MaxSessions <= 0 || cfg.MaxTotalBytes <= 0 || cfg.SweepInterval <= 0 {
		t.Fatalf("invalid bundled config: %+v", cfg)
	}
}
