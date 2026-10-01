package credentialstore

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// backendConformance checks credential persistence and isolation.
func backendConformance(t *testing.T, newBackend func(t *testing.T) Backend) {
	t.Helper()

	t.Run("empty namespace is not an error", func(t *testing.T) {
		snapshot, err := newBackend(t).Snapshot()
		testutil.FailErr(t, "Snapshot", err)
		if len(snapshot.Values) != 0 {
			t.Fatalf("fresh namespace holds %v", snapshot.Values)
		}
	})

	t.Run("round trip", func(t *testing.T) {
		b := newBackend(t)
		testutil.FailErr(t, "Set", b.Set("anthropic", "sk-first"))
		if got := snapshotValue(t, b, "anthropic"); got != "sk-first" {
			t.Fatalf("stored value = %q", got)
		}
	})

	t.Run("overwrite replaces rather than appends", func(t *testing.T) {
		b := newBackend(t)
		testutil.FailErr(t, "Set", b.Set("anthropic", "sk-first"))
		testutil.FailErr(t, "Set again", b.Set("anthropic", "sk-second"))
		if got := snapshotValue(t, b, "anthropic"); got != "sk-second" {
			t.Fatalf("after overwrite = %q; want the second value", got)
		}
		snapshot, err := b.Snapshot()
		testutil.FailErr(t, "Snapshot", err)
		if len(snapshot.Values) != 1 {
			t.Fatalf("overwrite left %d credentials", len(snapshot.Values))
		}
	})

	t.Run("delete is idempotent", func(t *testing.T) {
		b := newBackend(t)
		testutil.FailErr(t, "Set", b.Set("anthropic", "sk-first"))
		testutil.FailErr(t, "Delete", b.Delete("anthropic"))
		testutil.FailErr(t, "Delete absent", b.Delete("anthropic"))
		if got := snapshotValue(t, b, "anthropic"); got != "" {
			t.Fatalf("deleted credential still reads %q", got)
		}
	})

	t.Run("credentials are independent", func(t *testing.T) {
		b := newBackend(t)
		testutil.FailErr(t, "Set a", b.Set("anthropic", "sk-a"))
		testutil.FailErr(t, "Set b", b.Set("openai", "sk-b"))
		testutil.FailErr(t, "Delete a", b.Delete("anthropic"))
		if got := snapshotValue(t, b, "openai"); got != "sk-b" {
			t.Fatalf("deleting one credential disturbed another: %q", got)
		}
	})

	t.Run("large opaque value", func(t *testing.T) {
		b := newBackend(t)
		big := strings.Repeat("abcdefgh", 2048) // 16 KiB
		testutil.FailErr(t, "Set", b.Set("mcp-server", big))
		if got := snapshotValue(t, b, "mcp-server"); got != big {
			t.Fatalf("large value round-tripped at %d bytes; want %d", len(got), len(big))
		}
		testutil.FailErr(t, "shrink", b.Set("mcp-server", "small"))
		if got := snapshotValue(t, b, "mcp-server"); got != "small" {
			t.Fatalf("after shrinking = %q", got)
		}
	})

	t.Run("describe names the backend", func(t *testing.T) {
		if strings.TrimSpace(newBackend(t).Describe()) == "" {
			t.Fatal("Describe is empty; the recovery CLI reports it verbatim")
		}
	})
}

func snapshotValue(t *testing.T, b Backend, id string) string {
	t.Helper()
	snapshot, err := b.Snapshot()
	testutil.FailErr(t, "Snapshot", err)
	return snapshot.Values[id]
}

func TestEncryptedBackendConformance(t *testing.T) {
	backendConformance(t, func(t *testing.T) Backend {
		return newDevelopmentBackend(filepath.Join(t.TempDir(), VaultBasename), "conformance")
	})
}
