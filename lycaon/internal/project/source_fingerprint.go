package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Fingerprints depend on the selected entry, not read access to its parent.
func sourceTreeFingerprint(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	progress := newSourceWorkProgress(ctx, "verifying")
	defer progress.report(true)
	if info.IsDir() {
		err = fingerprintSourceDirectory(ctx, hash, path, info, progress)
	} else {
		err = fingerprintSourceEntry(ctx, hash, ".", info,
			func() (*os.File, error) { return os.Open(path) },
			func() (string, error) { return os.Readlink(path) }, progress)
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fingerprintSourceDirectory(ctx context.Context, hash io.Writer, path string, info fs.FileInfo, progress *sourceWorkProgress) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return ErrSourceMutationDiverged
	}
	return fs.WalkDir(root.FS(), ".", func(current string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := root.Lstat(current)
		if err != nil {
			return err
		}
		return fingerprintSourceEntry(ctx, hash, current, entry,
			func() (*os.File, error) { return root.Open(current) },
			func() (string, error) { return root.Readlink(current) }, progress)
	})
}

func fingerprintSourceEntry(ctx context.Context, hash io.Writer, rel string, info fs.FileInfo, open func() (*os.File, error), readlink func() (string, error), progress *sourceWorkProgress) error {
	entry := sourceledger.RecoveryEntry{Path: rel, Mode: uint32(info.Mode())}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := readlink()
		if err != nil {
			return err
		}
		entry.Link = target
	} else if info.Mode().IsRegular() {
		sha, err := fingerprintSourceFile(ctx, info, open, progress)
		if err != nil {
			return err
		}
		entry.SHA = sha
	}
	if err := writeSourceTreeEntry(hash, entry); err != nil {
		return err
	}
	progress.entry()
	return nil
}

func fingerprintSourceFile(ctx context.Context, info fs.FileInfo, open func() (*os.File, error), progress *sourceWorkProgress) (string, error) {
	file, err := open()
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !sameSourceFileVersion(info, opened) {
		return "", ErrSourceMutationDiverged
	}
	content := sha256.New()
	size, err := io.Copy(content, contextio.Reader{Context: ctx, Source: file, OnRead: progress.noteBytes})
	if err != nil {
		return "", err
	}
	after, err := file.Stat()
	if err != nil {
		return "", err
	}
	if size != info.Size() || !sameSourceFileVersion(opened, after) {
		return "", ErrSourceMutationDiverged
	}
	return hex.EncodeToString(content.Sum(nil)), nil
}

func sameSourceFileVersion(a, b fs.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
