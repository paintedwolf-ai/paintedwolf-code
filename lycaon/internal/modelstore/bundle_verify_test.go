package modelstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyBundleLeavesStoreAndSeedUnchanged(t *testing.T) {
	s, directory, signed := bundleFixture(t)
	manifest, err := VerifyBundle(t.Context(), directory, s.signer.(*Authority), Slot{Model: "e4b", Device: "mlx"})
	if err != nil || !sameManifest(manifest.Manifest, signed.Manifest) {
		t.Fatalf("verify seed: %+v, %v", manifest, err)
	}
	if _, err := os.Stat(s.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verification created a store: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("verification changed seed entries: %v, %v", entries, err)
	}
}

func TestVerifyBundleRefusesInvalidAuthoritySlotAndPayload(t *testing.T) {
	for _, fault := range []string{"authority", "signature", "slot", "content", "link", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			s, directory, signed := bundleFixture(t)
			authority := s.signer.(*Authority)
			slot := Slot{Model: "e4b", Device: "mlx"}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			path := filepath.Join(directory, signed.Manifest.Artifact.Name)
			switch fault {
			case "authority":
				authority = nil
			case "signature":
				signed.KeyID = "unknown"
				writeBundleManifest(t, directory, signed)
			case "slot":
				slot.Device = "vulkan"
			case "content":
				if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
					t.Fatalf("damage seed payload: %v", err)
				}
			case "link":
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove seed payload: %v", err)
				}
				if err := os.Symlink("manifest.json", path); err != nil {
					t.Fatalf("replace seed payload with link: %v", err)
				}
			case "canceled":
				cancel()
			}
			if _, err := VerifyBundle(ctx, directory, authority, slot); err == nil {
				t.Fatal("accepted invalid bundle")
			}
		})
	}
}
