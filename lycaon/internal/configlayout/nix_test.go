package configlayout

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func nixFixture(t *testing.T, output string, image []byte) string {
	t.Helper()
	root := filepath.Join(output, "lib", "painted-wolf-code")
	executable := filepath.Join(root, "bin", "pw")
	testutil.FailErr(t, "create fixture", os.MkdirAll(filepath.Dir(executable), 0o755))
	testutil.FailErr(t, "write image", os.WriteFile(executable, image, 0o555))
	receipt, err := json.Marshal(nixReceipt{FormatVersion: 1, Output: output, NixStore: "/tools/nix/bin/nix-store"})
	testutil.FailErr(t, "encode receipt", err)
	testutil.FailErr(t, "write receipt", os.WriteFile(filepath.Join(root, "nix-layout.json"), receipt, 0o444))
	digest := sha256.Sum256(image)
	manifest, err := json.Marshal(map[string]any{
		"format_version": 1, "package": "painted-wolf-code", "version": "1.0.2", "architecture": "x86_64", "install_root": root,
		"executables": []any{map[string]any{"path": executable, "sha256": hex.EncodeToString(digest[:]), "size": len(image)}},
	})
	testutil.FailErr(t, "encode manifest", err)
	testutil.FailErr(t, "write manifest", os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0o444))
	return executable
}

func TestNixGenerationUsesTheRunningImageAfterProfileSwitch(t *testing.T) {
	root := t.TempDir()
	first := nixFixture(t, filepath.Join(root, "store", "generation-A"), []byte("image A"))
	second := nixFixture(t, filepath.Join(root, "store", "generation-B"), []byte("image B"))
	// The kernel supplies the real ELF path; changing a profile cannot alter it.
	layout, err := nixLayout(first, bytes.NewReader([]byte("image A")))
	testutil.FailErr(t, "resolve running generation", err)
	if layout == nil || filepath.Join(layout.Root, "bin", "pw") != first {
		t.Fatalf("wrong generation: %+v", layout)
	}
	if _, err := nixLayout(second, bytes.NewReader([]byte("image A"))); err == nil {
		t.Fatal("accepted old descriptor against new generation")
	}
}

func TestNixLayoutRequiresExplicitReceiptAndExactManifest(t *testing.T) {
	executable := nixFixture(t, filepath.Join(t.TempDir(), "custom-store", "output"), []byte("image"))
	root := filepath.Dir(filepath.Dir(executable))
	testutil.FailErr(t, "make receipt replaceable", os.Chmod(filepath.Join(root, "nix-layout.json"), 0o600))
	for _, receipt := range []string{
		`{"format_version":1,"output":"relative","nix_store":"/tools/nix-store"}`,
		`{"format_version":2,"output":"/wrong","nix_store":"/tools/nix-store"}`,
		`{"format_version":1,"output":"/wrong","nix_store":"/tools/nix-store","trusted":true}`,
	} {
		testutil.FailErr(t, "replace receipt", os.WriteFile(filepath.Join(root, "nix-layout.json"), []byte(receipt), 0o600))
		if _, err := nixLayout(executable, bytes.NewReader([]byte("image"))); err == nil {
			t.Fatal("invalid present receipt became developer fallback")
		}
	}
	testutil.FailErr(t, "remove receipt", os.Remove(filepath.Join(root, "nix-layout.json")))
	layout, err := nixLayout(executable, bytes.NewReader([]byte("image")))
	if err != nil || layout != nil {
		t.Fatalf("store-looking paths must not classify ownership: %+v %v", layout, err)
	}
}

func TestNixGenerationRefusesChangedImageAndMalformedManifest(t *testing.T) {
	executable := nixFixture(t, filepath.Join(t.TempDir(), "generation"), []byte("image"))
	if _, err := nixLayout(executable, bytes.NewReader([]byte("changed"))); err == nil {
		t.Fatal("changed image accepted")
	}
	testutil.FailErr(t, "make manifest replaceable", os.Chmod(filepath.Join(filepath.Dir(filepath.Dir(executable)), "manifest.json"), 0o600))
	testutil.FailErr(t, "corrupt manifest", os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(executable)), "manifest.json"), []byte("{}"), 0o600))
	if _, err := nixLayout(executable, bytes.NewReader([]byte("image"))); err == nil {
		t.Fatal("unknown manifest accepted")
	}
}
