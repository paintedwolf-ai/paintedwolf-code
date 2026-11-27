package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBoundedExecutableSHA256Controls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "fixture-bin")
	payload := []byte("deterministic-fixture-executable-payload")
	testutil.FailErr(t, "write fixture binary", os.WriteFile(file, payload, 0o755))

	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])

	if got, status := boundedExecutableSHA256(t.Context(), file, int64(len(payload))); status != "ok" || got != want {
		t.Fatalf("exact budget: got=(%q,%q), want=(%q,ok)", got, status, want)
	}
	if got, status := boundedExecutableSHA256(t.Context(), file, int64(len(payload)-1)); status != "over_budget" || got != "" {
		t.Fatalf("over budget: got=(%q,%q), want=(\"\",over_budget)", got, status)
	}
	if got, status := boundedExecutableSHA256(t.Context(), filepath.Join(dir, "missing"), 1024); status != "unavailable" || got != "" {
		t.Fatalf("missing file: got=(%q,%q), want=(\"\",unavailable)", got, status)
	}
	if got, status := boundedExecutableSHA256(t.Context(), "", 1024); status != "unavailable" || got != "" {
		t.Fatalf("empty path: got=(%q,%q), want=(\"\",unavailable)", got, status)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, status := boundedExecutableSHA256(canceled, file, 1024); status != "canceled" || got != "" {
		t.Fatalf("canceled ctx: got=(%q,%q), want=(\"\",canceled)", got, status)
	}
}
