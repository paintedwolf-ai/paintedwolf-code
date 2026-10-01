package extpacks

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeIntegrityFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "pack")
	files := map[string]string{
		"extension.yaml": "manifest_version: 1\nid: acme/fixture\nname: fixture\nversion: 1.0.0\n" +
			"compatibility:\n  extension_api: ^1.0.0\n",
		"guidance/note.md":            "note\n",
		"guidance/enrich/deep.md":     "deep\n",
		"policy/FIXTURE_CODE.yaml":    "id: FIXTURE_CODE\n",
		"skills/demo/SKILL.md":        "skill\n",
		"skills/demo/scripts/run.sh":  "#!/bin/sh\n",
		"guidance/.hidden.md":         "hidden\n",
		"guidance/_draft.md":          "draft\n",
		"tools/vendor/dep.yaml":       "vendored\n",
		"not-a-kind-root/payload.txt": "payload\n",
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(path, []byte(body), 0o644))
	}
	return root
}

// The exec bit on a skill payload is behavior; it must be inside integrity.
func TestPackTreeIntegrityCoversExecBit(t *testing.T) {
	root := writeIntegrityFixture(t)
	before, err := PackTreeIntegrity(root)
	testutil.FailErr(t, "integrity before", err)

	script := filepath.Join(root, "skills", "demo", "scripts", "run.sh")
	testutil.FailErr(t, "chmod +x", os.Chmod(script, 0o755))
	after, err := PackTreeIntegrity(root)
	testutil.FailErr(t, "integrity after", err)
	if before == after {
		t.Fatal("flipping the exec bit must change pack integrity")
	}

	// Group/other bits follow the checkout umask and stay outside identity.
	testutil.FailErr(t, "chmod g-r", os.Chmod(script, 0o750))
	relaxed, err := PackTreeIntegrity(root)
	testutil.FailErr(t, "integrity relaxed", err)
	if relaxed != after {
		t.Fatal("group/other permission bits must not change pack integrity")
	}
}

func TestCopyDirPreservesOwnerExecutableBit(t *testing.T) {
	source := writeIntegrityFixture(t)
	script := filepath.Join(source, "skills", "demo", "scripts", "run.sh")
	testutil.FailErr(t, "chmod +x", os.Chmod(script, 0o755))
	destination := filepath.Join(t.TempDir(), "copy")
	testutil.FailErr(t, "copy pack", copyDir(source, destination))

	want, err := PackTreeIntegrity(source)
	testutil.FailErr(t, "source integrity", err)
	got, err := PackTreeIntegrity(destination)
	testutil.FailErr(t, "destination integrity", err)
	if got != want {
		t.Fatalf("copied integrity = %s want %s", got, want)
	}
	info, err := os.Stat(filepath.Join(destination, "skills", "demo", "scripts", "run.sh"))
	testutil.FailErr(t, "stat copied script", err)
	if info.Mode()&0o100 == 0 {
		t.Fatal("copy must preserve the owner executable bit")
	}
}

// The verified walk must produce exactly InventoryPack's unit set from the
// same bytes the integrity hash covered — one read, no second walk.
func TestWalkPackTreeMatchesInventoryAndIntegrity(t *testing.T) {
	root := writeIntegrityFixture(t)
	man, err := LoadManifest(root)
	testutil.FailErr(t, "load manifest", err)
	inventoried, err := InventoryPack(Pack{ID: "acme/fixture", Root: OnDisk(root)}, man)
	testutil.FailErr(t, "InventoryPack", err)
	wantIntegrity, err := PackTreeIntegrity(root)
	testutil.FailErr(t, "PackTreeIntegrity", err)

	walked, err := walkPackTree(root, "acme/kit", true)
	testutil.FailErr(t, "walkPackTree", err)
	if walked.Integrity != wantIntegrity {
		t.Fatalf("verified walk integrity %s != PackTreeIntegrity %s", walked.Integrity, wantIntegrity)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, "extension.yaml"))
	testutil.FailErr(t, "read manifest", err)
	if !bytes.Equal(walked.Manifest, manifestBytes) {
		t.Fatal("verified walk must capture the manifest bytes it hashed")
	}
	if len(walked.Units) != len(inventoried.Units) {
		t.Fatalf("verified walk units = %d, InventoryPack = %d", len(walked.Units), len(inventoried.Units))
	}
	for i, u := range inventoried.Units {
		w := walked.Units[i]
		if w.ID != u.ID || w.Kind != u.Kind || !bytes.Equal(w.Content, u.Content) {
			t.Fatalf("unit %d mismatch: walked %s/%s vs inventoried %s/%s", i, w.ID, w.Kind, u.ID, u.Kind)
		}
	}
}
