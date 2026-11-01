package modelstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fssync"
)

// Stage copies a payload into the store under its digest after verifying size, hash, and disk space.
func (s *Store) Stage(r io.Reader, want Artifact) error {
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
	defer root.Close()
	dir := filepath.Join("artifacts", want.Digest[:2])
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("modelstore: mkdir: %w", err)
	}
	tmpName := filepath.Join(dir, ".staging-"+rand.Text())
	tmp, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("modelstore: temp: %w", err)
	}
	defer root.Remove(tmpName)

	h := sha256.New()
	limited := io.LimitReader(r, want.Bytes+1)
	n, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("modelstore: copy: %w", err)
	}
	if n != want.Bytes {
		tmp.Close()
		return fmt.Errorf("modelstore: size mismatch: got %d want %d", n, want.Bytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want.Digest {
		tmp.Close()
		return fmt.Errorf("modelstore: digest mismatch: got %s want %s", got, want.Digest)
	}
	if err := fssync.File(tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("modelstore: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelstore: close: %w", err)
	}
	if err := root.Rename(tmpName, s.artifactRel(want.Digest)); err != nil {
		return fmt.Errorf("modelstore: publish: %w", err)
	}
	dfd, err := root.Open(dir)
	if err != nil {
		return fmt.Errorf("modelstore: publish dir: %w", err)
	}
	if err := fssync.File(dfd); err != nil {
		dfd.Close()
		return fmt.Errorf("modelstore: publish dir sync: %w", err)
	}
	if err := dfd.Close(); err != nil {
		return fmt.Errorf("modelstore: publish dir close: %w", err)
	}
	return nil
}

// Verified reports whether an artifact is present on disk with the expected sha256 digest.
func (s *Store) Verified(digest string) bool {
	if !digestShape.MatchString(digest) {
		return false
	}
	root, err := s.openRoot()
	if err != nil {
		return false
	}
	defer root.Close()
	f, err := root.Open(s.artifactRel(digest))
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == digest
}
