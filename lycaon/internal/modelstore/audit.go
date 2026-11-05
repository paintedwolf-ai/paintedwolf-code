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

// Audit hashes the pinned revision. The caller must retain its lease until completion.
func (l *Lease) Audit(ctx context.Context) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil || l.lock == nil {
		return errors.New("modelstore: audit requires a revision lease")
	}
	if _, err := l.lock.Stat(); err != nil {
		return fmt.Errorf("modelstore: audit lease: %w", err)
	}
	root, err := os.OpenRoot(l.Dir)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	for name, artifact := range manifestFiles(l.Manifest) {
		if err := auditFile(ctx, root, name, artifact); err != nil {
			return fmt.Errorf("modelstore: audit %s: %w", name, err)
		}
	}
	return nil
}

func auditFile(ctx context.Context, root *os.Root, name string, artifact Artifact) (resultErr error) {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.Bytes {
		return errors.New("payload shape differs from manifest")
	}
	f, err := openBundleFile(root, name, artifact.Bytes)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, f.Close()) }()
	hash := sha256.New()
	n, err := io.Copy(hash, contextReader{ctx: ctx, reader: io.LimitReader(f, artifact.Bytes+1)})
	if err != nil {
		return err
	}
	if n != artifact.Bytes || hex.EncodeToString(hash.Sum(nil)) != artifact.Digest {
		return errors.New("payload content differs from manifest")
	}
	return nil
}
