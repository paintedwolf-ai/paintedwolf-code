package cadence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectignore"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIgnoreArtifactRequiresExplicitCurrentFormat(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create overlay", os.MkdirAll(settingsoverlay.Dir(root), 0o750))
	original := []byte("version: 1\nfindings:\n  - id: existing\n    path: test/**\n    reason: reviewed fixture\n")
	path := projectignore.Path(root)
	testutil.FailErr(t, "write unmarked ignore file", os.WriteFile(path, original, 0o644))
	_, err := scanignore.LoadIgnoreCatalog([]string{root})
	if !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("unmarked ignore file loaded: %v", err)
	}
	_, err = scanignore.AddIgnoreEntry(root, scanignore.IgnoreEntry{Path: "vendor/**", Reason: "reviewed dependency"})
	if !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("write silently advanced an unmarked ignore file: %v", err)
	}
	if err := scanignore.RemoveIgnoreEntry(root, "existing"); !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("removal silently advanced an unmarked ignore file: %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused ignore file", err)
	if !bytes.Equal(original, after) {
		t.Fatal("refusal changed the ignore decisions")
	}
	testutil.FailErr(t, "record reviewed format explicitly", os.WriteFile(settingsoverlay.FormatPath(root), []byte("overlay_format: 3\n"), 0o644))
	_, err = scanignore.LoadIgnoreCatalog([]string{root})
	testutil.FailErr(t, "load reviewed ignore file", err)
}

func TestIgnoreCompatibilityCannotBeMaskedByTrustFiltering(t *testing.T) {
	root := ledgerRoot(t)
	testutil.FailErr(t, "create overlay", os.MkdirAll(settingsoverlay.Dir(root), 0o750))
	testutil.FailErr(t, "write unmarked ignore file", os.WriteFile(projectignore.Path(root), []byte("version: 1\nfindings: []\n"), 0o644))
	cadence := &Service{Store: ledgerFixture(t, root, "sast", "execution-1"),
		OverlayRootsApply: func(context.Context, []string) []string { return nil }}
	if _, err := cadence.ListIgnores(t.Context(), root); !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("trust filtering hid ignore incompatibility: %v", err)
	}
	if _, err := cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{}); !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("ledger replaced incompatible decisions with an empty set: %v", err)
	}
	ingester := &scanbase.IngesterImpl{OverlayRootsApply: func(context.Context, []string) []string { return nil }}
	_, err := ingester.Ingest(t.Context(), scanbase.ScanSourceRegistry, nil, scanbase.IngestMeta{
		ProjectDir: root,
		Scanner: scancatalog.ScannerContract{ScannerID: "test", Engine: "test-engine", Driver: scancatalog.DriverLibrary,
			Scope: scancatalog.ScopeSourceDriver, DefinitionFingerprint: strings.Repeat("a", 64)},
	})
	if !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("ingest trust filtering hid incompatible decisions: %v", err)
	}
}

func TestIgnorePublicationWaitsForFormatMarker(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create overlay", os.MkdirAll(settingsoverlay.Dir(root), 0o750))
	target := filepath.Join(t.TempDir(), "format.yaml")
	original := []byte("overlay_format: 1\n")
	testutil.FailErr(t, "write external marker", os.WriteFile(target, original, 0o644))
	testutil.FailErr(t, "link format marker", os.Symlink(target, settingsoverlay.FormatPath(root)))
	if _, err := scanignore.AddIgnoreEntry(root, scanignore.IgnoreEntry{Path: "test/**", Reason: "fixture"}); err == nil {
		t.Fatal("writer replaced a linked format marker")
	}
	if _, err := os.Lstat(projectignore.Path(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignore file published before its marker: %v", err)
	}
	after, err := os.ReadFile(target)
	testutil.FailErr(t, "read external marker", err)
	if !bytes.Equal(original, after) {
		t.Fatal("marker publication changed the symlink target")
	}
}
