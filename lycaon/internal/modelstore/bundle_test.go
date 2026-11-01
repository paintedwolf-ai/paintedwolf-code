package modelstore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundleFixture(t *testing.T) (*Store, string, SignedManifest) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate bundle key: %v", err)
	}
	authority, err := NewAuthority(map[string]ed25519.PublicKey{"fixture": public})
	if err != nil {
		t.Fatalf("create bundle authority: %v", err)
	}
	a := testArtifact(t, []byte("weights"))
	a.Name = "model.safetensors"
	m := Manifest{Model: "e4b", Device: "mlx", Revision: "r1", Artifact: a}
	raw, err := SigningBytes(m)
	if err != nil {
		t.Fatalf("encode bundle signature input: %v", err)
	}
	signed := SignedManifest{Manifest: m, KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))}
	dir := t.TempDir()
	writeBundleManifest(t, dir, signed)
	if err := os.WriteFile(filepath.Join(dir, a.Name), []byte("weights"), 0o600); err != nil {
		t.Fatalf("write bundle weights: %v", err)
	}
	return New(filepath.Join(t.TempDir(), "store"), authority), dir, signed
}

func writeBundleManifest(t *testing.T, dir string, signed SignedManifest) {
	t.Helper()
	raw, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("encode bundle manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatalf("write bundle manifest: %v", err)
	}
}

func TestBundleImportAndRepair(t *testing.T) {
	s, dir, signed := bundleFixture(t)
	if err := s.ImportBundle(context.Background(), dir); err != nil {
		t.Fatalf("import bundle: %v", err)
	}
	lease, err := s.AcquireLease("e4b", "mlx", "fixture", 1)
	if err != nil {
		t.Fatalf("lease imported bundle: %v", err)
	}
	if err := s.RepairBundle(context.Background(), dir); !errors.Is(err, ErrRevisionInUse) {
		t.Fatalf("repair leased bundle: %v", err)
	}
	lease.Release()
	path := filepath.Join(s.RevisionDir(signed.Manifest), "model.safetensors")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove fixture payload: %v", err)
	}
	if err := s.RepairBundle(context.Background(), dir); err != nil {
		t.Fatalf("repair bundle: %v", err)
	}
	if got := s.Status("e4b", "mlx"); got.State != StateReady {
		t.Fatalf("repaired readiness: %+v", got)
	}
}

func TestBundleRejectsUntrustedManifestBeforeMutation(t *testing.T) {
	s, dir, signed := bundleFixture(t)
	signed.KeyID = "untrusted"
	writeBundleManifest(t, dir, signed)
	if err := os.Remove(filepath.Join(dir, signed.Manifest.Artifact.Name)); err != nil {
		t.Fatalf("remove untrusted payload: %v", err)
	}
	if err := s.ImportBundle(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "unknown signing key") {
		t.Fatalf("untrusted import: %v", err)
	}
	if _, err := os.Stat(s.Root()); !os.IsNotExist(err) {
		t.Fatalf("untrusted bundle mutated store: %v", err)
	}
}

func TestBundleManifestBounds(t *testing.T) {
	for _, change := range []string{"unknown", "trailing", "oversize", "unnamed", "reserved"} {
		t.Run(change, func(t *testing.T) {
			s, dir, signed := bundleFixture(t)
			raw, err := json.Marshal(signed)
			if err != nil {
				t.Fatalf("encode bound fixture: %v", err)
			}
			switch change {
			case "unknown":
				raw = append([]byte(`{"extra":1,`), raw[1:]...)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			case "oversize":
				raw = append(raw, []byte(strings.Repeat(" ", maxBundleManifestBytes))...)
			case "unnamed", "reserved":
				signed.Manifest.Artifact.Name = ""
				if change == "reserved" {
					signed.Manifest.Artifact.Name = "manifest.json"
				}
				writeBundleManifest(t, dir, signed)
				raw = nil
			}
			if raw != nil {
				if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
					t.Fatalf("write bound fixture: %v", err)
				}
			}
			if err := s.ImportBundle(context.Background(), dir); err == nil {
				t.Fatal("invalid bundle manifest accepted")
			}
			if _, err := os.Stat(s.Root()); !os.IsNotExist(err) {
				t.Fatalf("invalid manifest mutated store: %v", err)
			}
		})
	}
}

func TestBundleRejectsPayloadDamageAndLinks(t *testing.T) {
	for _, change := range []string{"digest", "short", "oversize", "symlink", "directory"} {
		t.Run(change, func(t *testing.T) {
			s, dir, signed := bundleFixture(t)
			path := filepath.Join(dir, signed.Manifest.Artifact.Name)
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove payload fixture: %v", err)
			}
			var err error
			switch change {
			case "symlink":
				err = os.Symlink(filepath.Join(dir, "manifest.json"), path)
			case "directory":
				err = os.Mkdir(path, 0o700)
			default:
				data := map[string]string{"digest": "changed", "short": "x", "oversize": "longer weights"}[change]
				err = os.WriteFile(path, []byte(data), 0o600)
			}
			if err != nil {
				t.Fatalf("change payload fixture: %v", err)
			}
			if err := s.ImportBundle(context.Background(), dir); err == nil {
				t.Fatal("invalid bundle payload accepted")
			}
			if got := s.Status("e4b", "mlx"); got.State != StateAbsent {
				t.Fatalf("invalid bundle activated: %+v", got)
			}
		})
	}
}
