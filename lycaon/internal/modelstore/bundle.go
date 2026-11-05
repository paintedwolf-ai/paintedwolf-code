package modelstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxBundleManifestBytes = 128 << 10

type Slot struct {
	Model  string
	Device string
}

// ImportBundle authenticates manifest.json before opening its named payloads.
func (s *Store) ImportBundle(ctx context.Context, directory string, target Slot) error {
	return s.importBundle(ctx, directory, target, false)
}

// RepairBundle retains the active revision until its replacement is complete.
func (s *Store) RepairBundle(ctx context.Context, directory string, target Slot) error {
	return s.importBundle(ctx, directory, target, true)
}

func (s *Store) importBundle(ctx context.Context, directory string, target Slot, repair bool) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("modelstore: open bundle: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	signed, err := readBundleManifest(root)
	if err != nil {
		return err
	}
	if signed.Manifest.Model != target.Model || signed.Manifest.Device != target.Device {
		return fmt.Errorf("modelstore: bundle does not match target slot %q/%q", target.Model, target.Device)
	}
	return s.importRevision(ctx, signed, func(_ context.Context, a Artifact) (io.ReadCloser, error) {
		return openBundleFile(root, a.Name, a.Bytes)
	}, repair)
}

func readBundleManifest(root *os.Root) (signed SignedManifest, resultErr error) {
	f, err := openBundleFile(root, "manifest.json", maxBundleManifestBytes)
	if err != nil {
		return signed, err
	}
	defer func() { resultErr = errors.Join(resultErr, f.Close()) }()
	decoder := json.NewDecoder(io.LimitReader(f, maxBundleManifestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&signed); err != nil {
		return signed, fmt.Errorf("modelstore: decode bundle manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return signed, errors.New("modelstore: trailing bundle manifest content")
	}
	if err := validate(signed.Manifest); err != nil {
		return signed, err
	}
	for _, a := range manifestFiles(signed.Manifest) {
		if a.Name == "" || a.Name == "manifest.json" {
			return signed, errors.New("modelstore: bundle payloads require explicit, non-reserved names")
		}
	}
	return signed, nil
}

func openBundleFile(root *os.Root, name string, maxBytes int64) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, fmt.Errorf("modelstore: invalid bundle file %s", name)
	}
	f, err := openBundlePayload(root, name)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.Join(fmt.Errorf("modelstore: bundle file changed while opening %s", name), err, f.Close())
	}
	return f, nil
}
