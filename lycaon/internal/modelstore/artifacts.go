package modelstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
)

// Stage copies a payload into the store under its digest after verifying size, hash, and disk space.
func (s *Store) Stage(r io.Reader, want Artifact) (resultErr error) {
	if err := validateArtifact(want); err != nil {
		return err
	}
	if err := s.CheckSpace(SpaceRequirement{StagingBytes: want.Bytes}); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	dir := filepath.Join("artifacts", want.Digest[:2])
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("modelstore: mkdir: %w", err)
	}
	tmpName := filepath.Join(dir, ".staging-"+rand.Text())
	tmp, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("modelstore: temp: %w", err)
	}
	defer func() {
		if err := root.Remove(tmpName); err != nil && !errors.Is(err, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("modelstore: remove staged payload: %w", err))
		}
	}()
	if err := sealStagedPayload(tmp, r, want); err != nil {
		return err
	}
	if err := root.Rename(tmpName, s.artifactRel(want.Digest)); err != nil {
		return fmt.Errorf("modelstore: publish: %w", err)
	}
	if err := syncPublicationDirectories(root, dir, fseffect.SyncDirectory); err != nil {
		return fmt.Errorf("modelstore: publish directories: %w", err)
	}
	return nil
}

// sealStagedPayload closes the verified bytes before publication.
func sealStagedPayload(tmp *os.File, r io.Reader, want Artifact) (resultErr error) {
	defer func() { resultErr = errors.Join(resultErr, tmp.Close()) }()
	h := sha256.New()
	limited := io.LimitReader(r, want.Bytes+1)
	n, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		return fmt.Errorf("modelstore: copy: %w", err)
	}
	if n != want.Bytes {
		return fmt.Errorf("modelstore: size mismatch: got %d want %d", n, want.Bytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want.Digest {
		return fmt.Errorf("modelstore: digest mismatch: got %s want %s", got, want.Digest)
	}
	if err := tmp.Chmod(0o400); err != nil {
		return fmt.Errorf("modelstore: seal payload: %w", err)
	}
	if err := fssync.File(tmp); err != nil {
		return fmt.Errorf("modelstore: sync: %w", err)
	}
	return nil
}

// Verified reports whether an artifact is present on disk with the expected sha256 digest.
func (s *Store) Verified(digest string) (verified bool) {
	if !digestShape.MatchString(digest) {
		return false
	}
	root, err := s.openRoot()
	if err != nil {
		return false
	}
	defer func() {
		if err := root.Close(); err != nil {
			verified = false
		}
	}()
	f, err := root.Open(s.artifactRel(digest))
	if err != nil {
		return false
	}
	defer func() {
		if err := f.Close(); err != nil {
			verified = false
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}
