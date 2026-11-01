package modelstore

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
)

// SealBundle authenticates an export only after its declared payloads pass audit.
func SealBundle(ctx context.Context, directory string, manifest Manifest, authority *Authority, keyID string, private ed25519.PrivateKey) (SignedManifest, error) {
	var empty SignedManifest
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if authority == nil || len(private) != ed25519.PrivateKeySize {
		return empty, errors.New("modelstore: sealing requires a release authority and Ed25519 private key")
	}
	raw, err := SigningBytes(manifest)
	if err != nil {
		return empty, err
	}
	signed := SignedManifest{Manifest: manifest, KeyID: keyID,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))}
	if err := authority.Verify(signed); err != nil {
		return empty, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	for name, artifact := range manifestFiles(manifest) {
		if artifact.Name == "" || name == "manifest.json" {
			return empty, errors.New("modelstore: sealed payloads require explicit, non-reserved names")
		}
		if err := auditFile(ctx, root, name, artifact); err != nil {
			return empty, fmt.Errorf("modelstore: export payload %s: %w", name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return signed, nil
}
