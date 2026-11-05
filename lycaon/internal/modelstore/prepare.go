package modelstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// PrepareBundle measures explicitly named exports without changing their bytes.
func PrepareBundle(ctx context.Context, directory string, recipe Manifest) (Manifest, error) {
	var empty Manifest
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := validateIdentity(recipe); err != nil {
		return empty, err
	}
	if len(recipe.Files) > 64 {
		return empty, errors.New("modelstore: too many export payloads")
	}
	payloads := append([]Artifact{recipe.Artifact}, recipe.Files...)
	seen := make(map[string]bool)
	for _, payload := range payloads {
		if !nameShape.MatchString(payload.Name) || payload.Name == "manifest.json" || seen[payload.Name] {
			return empty, errors.New("modelstore: export payloads require unique explicit non-reserved names")
		}
		if payload.Digest != "" || payload.Bytes != 0 || !nameShape.MatchString(payload.Format) {
			return empty, errors.New("modelstore: export recipe requires a format and unmeasured payloads")
		}
		seen[payload.Name] = true
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	for i, payload := range payloads {
		measured, err := measurePayload(ctx, root, payload)
		if err != nil {
			return empty, fmt.Errorf("modelstore: prepare %s: %w", payload.Name, err)
		}
		payloads[i] = measured
	}
	manifest := recipe
	manifest.Artifact = payloads[0]
	manifest.Files = append([]Artifact(nil), payloads[1:]...)
	if err := validate(manifest); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return manifest, nil
}

func measurePayload(ctx context.Context, root *os.Root, payload Artifact) (Artifact, error) {
	file, err := openBundleFile(root, payload.Name, MaxArtifactBytes)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.New()
	count, err := io.Copy(hash, contextReader{ctx: ctx, reader: io.LimitReader(file, before.Size()+1)})
	if err != nil {
		return Artifact{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return Artifact{}, err
	}
	if count != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return Artifact{}, errors.New("export changed during measurement")
	}
	payload.Bytes = count
	payload.Digest = hex.EncodeToString(hash.Sum(nil))
	return payload, validateArtifact(payload)
}
