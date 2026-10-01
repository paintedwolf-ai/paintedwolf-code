package settingsoverlay

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEnsureCurrentFormatPreservesOverlayMetadata(t *testing.T) {
	root := t.TempDir()
	writeFormat(t, root, "# Project settings\noverlay_format: 1 # Reviewed format\ncustom:\n  name: \"kept\" # Keep this comment\n")
	testutil.FailErr(t, "publish current format", EnsureCurrentFormat(root))
	data, err := os.ReadFile(FormatPath(root))
	testutil.FailErr(t, "read format marker", err)
	for _, want := range []string{"# Project settings", "overlay_format: 3 # Reviewed format", "custom:", `name: "kept" # Keep this comment`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("updated marker lost %q: %s", want, data)
		}
	}
	testutil.FailErr(t, "ensure current marker again", EnsureCurrentFormat(root))
	after, err := os.ReadFile(FormatPath(root))
	testutil.FailErr(t, "read unchanged marker", err)
	if !bytes.Equal(data, after) {
		t.Fatal("current marker was rewritten")
	}
}

func TestEnsureCurrentFormatCreatesMissingMarker(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "publish new marker", EnsureCurrentFormat(root))
	format, err := ReadFormat(root)
	testutil.FailErr(t, "read published format", err)
	if format != MaxFormat {
		t.Fatalf("format = %d, want %d", format, MaxFormat)
	}
}

func TestEnsureCurrentFormatUpgradesWhenIgnoresExist(t *testing.T) {
	t.Run("missing overlay marker", func(t *testing.T) {
		root := t.TempDir()
		ignoresPath := filepath.Join(Dir(root), BasenameIgnores)
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(ignoresPath), 0o755))
		testutil.FailErr(t, "write ignores", os.WriteFile(ignoresPath, []byte("version: 1\nfindings: []\n"), 0o644))

		if err := CheckFormat(root); !errors.Is(err, ErrFormatInvalid) {
			t.Fatalf("expected CheckFormat to reject un-marked ignores, got %v", err)
		}
		testutil.FailErr(t, "ensure current format", EnsureCurrentFormat(root))
		testutil.FailErr(t, "check format after ensure", CheckFormat(root))
		format, err := ReadFormat(root)
		testutil.FailErr(t, "read format", err)
		if format != MaxFormat {
			t.Fatalf("format = %d, want %d", format, MaxFormat)
		}
	})

	t.Run("older overlay marker", func(t *testing.T) {
		root := t.TempDir()
		writeFormat(t, root, "overlay_format: 1\n")
		ignoresPath := filepath.Join(Dir(root), BasenameIgnores)
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(ignoresPath), 0o755))
		testutil.FailErr(t, "write ignores", os.WriteFile(ignoresPath, []byte("version: 1\nfindings: []\n"), 0o644))

		if err := CheckFormat(root); !errors.Is(err, ErrFormatInvalid) {
			t.Fatalf("expected CheckFormat to reject un-marked ignores, got %v", err)
		}
		testutil.FailErr(t, "ensure current format", EnsureCurrentFormat(root))
		testutil.FailErr(t, "check format after ensure", CheckFormat(root))
		format, err := ReadFormat(root)
		testutil.FailErr(t, "read format", err)
		if format != MaxFormat {
			t.Fatalf("format = %d, want %d", format, MaxFormat)
		}
	})
}

