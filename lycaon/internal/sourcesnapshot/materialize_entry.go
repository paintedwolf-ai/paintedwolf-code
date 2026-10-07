package sourcesnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

func (m *materializer) writeEntry(ctx context.Context, entry Entry) error {
	if entry.RootPath != m.root {
		return nil
	}
	if !filepath.IsLocal(filepath.FromSlash(entry.Path)) {
		return fmt.Errorf("invalid snapshot path %q", entry.Path)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target := filepath.Join(m.dir, filepath.FromSlash(entry.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400|os.FileMode(entry.Mode)&0o111)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	// Stream live content so large source files do not determine memory use.
	err = copyLiveVerified(ctx, entry, output)
	if errors.Is(err, ErrContentUnavailable) {
		raw, readErr := m.store.Bytes(ctx, entry)
		if readErr != nil {
			return fmt.Errorf("materialize %s: %w", entry.Path, readErr)
		}
		if err := output.Truncate(0); err != nil {
			return err
		}
		if _, err := output.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err = output.Write(raw)
	}
	if err != nil {
		return err
	}
	return output.Close()
}

func copyLiveVerified(ctx context.Context, entry Entry, output io.Writer) error {
	abs := absPath(entry.RootPath, entry.Path)
	before, err := os.Lstat(abs)
	if err != nil || !entry.liveMetadataCompatible(before) {
		return ErrContentUnavailable
	}
	input, err := os.Open(abs)
	if err != nil {
		return ErrContentUnavailable
	}
	defer func() { _ = input.Close() }()
	digest := sha256.New()
	gitDigest := sourceblob.GitBlobSHA1(entry.Size)
	n, err := io.Copy(io.MultiWriter(output, digest, gitDigest), contextio.Reader{Context: ctx, Source: input})
	if err != nil {
		return err
	}
	after, err := input.Stat()
	if err != nil || n != entry.Size || !sameFileVersion(before, after) {
		return ErrContentUnavailable
	}
	if entry.SHA256 != "" && hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
		return ErrContentUnavailable
	}
	if entry.GitOID != "" && hex.EncodeToString(gitDigest.Sum(nil)) != entry.GitOID {
		return ErrContentUnavailable
	}
	return nil
}
