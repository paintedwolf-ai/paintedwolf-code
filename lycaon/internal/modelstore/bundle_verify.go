package modelstore

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// VerifyBundle checks a packaged seed without creating or changing a store.
func VerifyBundle(ctx context.Context, directory string, authority *Authority, target Slot) (result SignedManifest, resultErr error) {
	var empty SignedManifest
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if authority == nil {
		return empty, errors.New("modelstore: bundle verification requires a release authority")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return empty, err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	signed, err := readBundleManifest(root)
	if err != nil {
		return empty, err
	}
	if signed.Manifest.Model != target.Model || signed.Manifest.Device != target.Device {
		return empty, fmt.Errorf("modelstore: bundle does not match target slot %q/%q", target.Model, target.Device)
	}
	if err := authority.Verify(signed); err != nil {
		return empty, err
	}
	for name, artifact := range manifestFiles(signed.Manifest) {
		if err := auditFile(ctx, root, name, artifact); err != nil {
			return empty, fmt.Errorf("modelstore: bundle payload %s: %w", name, err)
		}
	}
	return signed, ctx.Err()
}
