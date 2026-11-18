package modelstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSwitchSpaceAdmission(t *testing.T) {
	for _, mode := range []string{"room", "evict", "short", "leased", "external_link", "source_damage"} {
		t.Run(mode, func(t *testing.T) {
			s, dir, signed := bundleFixture(t)
			s.signer = acceptSigner{}
			slot := Slot{signed.Manifest.Model, signed.Manifest.Device}
			testutil.FailErr(t, "install existing revision", s.ImportFromBundlePath(t.Context(), dir, slot, nil))
			oldPath := filepath.Join(s.root, s.artifactRel(signed.Manifest.Artifact.Digest))
			m := signed.Manifest
			payload := bytes.Repeat([]byte("x"), 100)
			m.Revision = "next"
			m.Artifact = testArtifact(t, payload)
			m.Artifact.Name = "model.safetensors"
			writeBundleManifest(t, dir, SignedManifest{Manifest: m})
			testutil.FailErr(t, "new payload", os.WriteFile(filepath.Join(dir, m.Artifact.Name), payload, 0600))
			switch mode {
			case "external_link":
				testutil.FailErr(t, "retain old inode", os.Link(oldPath, filepath.Join(t.TempDir(), "retained")))
			case "leased":
				lease, err := s.AcquireLease(slot.Model, slot.Device, "live", 1)
				testutil.FailErr(t, "lease old revision", err)
				defer lease.Release()
			case "source_damage":
				testutil.FailErr(t, "damage new bundle", os.WriteFile(filepath.Join(dir, m.Artifact.Name), bytes.Repeat([]byte("y"), 100), 0600))
			}
			s.SetFreeBytesFunc(func(string) (int64, error) {
				available := int64(installReserve + len(payload))
				if _, err := os.Stat(oldPath); err == nil && mode != "room" {
					available -= signed.Manifest.Artifact.Bytes
				}
				if mode == "short" {
					available--
				}
				return available, nil
			})
			err := s.ImportFromBundlePath(t.Context(), dir, slot, nil)
			got, readErr := s.Selection()
			testutil.FailErr(t, "read selection", readErr)
			switch mode {
			case "evict", "room":
				testutil.FailErr(t, "switch tier", err)
				if got.Revision != m.Revision || s.Verified(signed.Manifest.Artifact.Digest) {
					t.Fatalf("switch left %+v or prior payload", got)
				}
			default:
				if err == nil || got.Revision != signed.Manifest.Revision || !s.Verified(signed.Manifest.Artifact.Digest) {
					t.Fatalf("lost old revision: %+v, %v", got, err)
				}
				if mode == "leased" && !errors.Is(err, ErrRevisionInUse) {
					t.Fatalf("lease result: %v", err)
				}
			}
		})
	}
}
