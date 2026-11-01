package modelstore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSealBundleAuditsPayloadBeforeReturningSignature(t *testing.T) {
	for _, fault := range []string{"none", "content", "link", "authority", "key", "unknown_key", "unnamed", "reserved", "traversal", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			_, directory, original := bundleFixture(t)
			public, private, err := ed25519.GenerateKey(rand.Reader)
			testutil.FailErr(t, "create fixture release key", err)
			authority, err := NewAuthority(map[string]ed25519.PublicKey{"fixture": public})
			testutil.FailErr(t, "create release authority", err)
			manifest := original.Manifest
			keyID := "fixture"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			payload := filepath.Join(directory, manifest.Artifact.Name)
			switch fault {
			case "content":
				testutil.FailErr(t, "change export", os.WriteFile(payload, []byte("changed"), 0o600))
			case "link":
				testutil.FailErr(t, "remove payload", os.Remove(payload))
				testutil.FailErr(t, "link payload", os.Symlink("manifest.json", payload))
			case "authority":
				authority = nil
			case "key":
				private = nil
			case "unknown_key":
				keyID = "other"
			case "unnamed":
				manifest.Artifact.Name = ""
			case "reserved":
				manifest.Artifact.Name = "manifest.json"
			case "traversal":
				manifest.Artifact.Name = "../weights"
			case "canceled":
				cancel()
			}
			signed, err := SealBundle(ctx, directory, manifest, authority, keyID, private)
			if fault != "none" {
				if err == nil || signed.Signature != "" {
					t.Fatalf("signed invalid export: %+v, %v", signed, err)
				}
				return
			}
			testutil.FailErr(t, "seal export", err)
			testutil.FailErr(t, "verify release signature", authority.Verify(signed))
			writeBundleManifest(t, directory, signed)
			_, err = VerifyBundle(ctx, directory, authority, Slot{Model: manifest.Model, Device: manifest.Device})
			testutil.FailErr(t, "verify sealed bundle", err)
		})
	}
}
