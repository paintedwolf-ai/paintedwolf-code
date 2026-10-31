package blobstore

import (
	"errors"
	"os"
	"path/filepath"
)

// HasRetentionClaim checks one existing exact reference without renewing it.
// Durable recovery must not manufacture a replacement claim marker.
func (s Store) HasRetentionClaim(claim, id string) (bool, error) {
	if !s.Available() {
		return false, ErrNoStore
	}
	if !isHexDigest(id) {
		return false, ErrNotFound
	}
	marker, err := retentionClaimID(claim)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(filepath.Join(s.dataRoot(), retentionDir, id, marker))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
