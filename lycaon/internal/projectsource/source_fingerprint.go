package projectsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type sourceTreeSize struct {
	Bytes, Files, AllocatedBytes int64
	AllocationKnown              bool
}

// Fingerprints depend on the selected entry, not read access to its parent.
func sourceTreeFingerprint(ctx context.Context, path string, memory sourceeffect.Memory) (digest string, size sourceTreeSize, fingerprintErr error) {
	if err := ctx.Err(); err != nil {
		return "", sourceTreeSize{}, err
	}
	if memory == nil {
		return "", sourceTreeSize{}, errors.New("source operation memory is required")
	}
	scratchLease, err := memory.Reserve(ctx, 36<<10)
	if err != nil {
		return "", sourceTreeSize{}, err
	}
	defer scratchLease()
	info, err := os.Lstat(path)
	if err != nil {
		return "", sourceTreeSize{}, err
	}
	scratch := make([]byte, 32<<10)
	hash := sha256.New()
	allocation := &sourceTreeAllocation{known: true, memory: memory}
	defer func() { fingerprintErr = errors.Join(fingerprintErr, allocation.close()) }()
	progress := newSourceWorkProgress(ctx, "verifying")
	defer progress.report(true)
	if info.IsDir() {
		err = fingerprintSourceDirectory(ctx, hash, path, info, progress, allocation, scratch)
	} else {
		err = fingerprintSourceEntry(ctx, hash, ".", info,
			func() (*os.File, error) { return os.Open(path) },
			func() (string, error) { return os.Readlink(path) }, progress, allocation, scratch)
	}
	if err != nil {
		return "", sourceTreeSize{}, err
	}
	return hex.EncodeToString(hash.Sum(nil)), sourceTreeSize{Bytes: progress.bytes, Files: progress.files, AllocatedBytes: allocation.bytes, AllocationKnown: allocation.known}, nil
}

func fingerprintSourceDirectory(ctx context.Context, hash io.Writer, path string, info fs.FileInfo, progress *sourceWorkProgress, allocation *sourceTreeAllocation, scratch []byte) error {
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
	return walkSourceTree(ctx, root, ".", allocation.memory, func(current string, entry fs.FileInfo) error {
		return fingerprintSourceEntry(ctx, hash, current, entry,
			func() (*os.File, error) { return root.Open(current) },
			func() (string, error) { return root.Readlink(current) }, progress, allocation, scratch)
	})
}

func fingerprintSourceEntry(ctx context.Context, hash io.Writer, rel string, info fs.FileInfo, open func() (*os.File, error), readlink func() (string, error), progress *sourceWorkProgress, allocation *sourceTreeAllocation, scratch []byte) error {
	if err := allocation.observe(ctx, info); err != nil {
		return err
	}
	entry := sourceledger.RecoveryEntry{Path: rel, Mode: uint32(info.Mode())}
	if !info.IsDir() {
		progress.files++
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := readlink()
		if err != nil {
			return err
		}
		entry.Link = target
	} else if info.Mode().IsRegular() {
		sha, err := fingerprintSourceFile(ctx, info, open, progress, scratch)
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

func fingerprintSourceFile(ctx context.Context, info fs.FileInfo, open func() (*os.File, error), progress *sourceWorkProgress, scratch []byte) (string, error) {
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
	size, err := io.CopyBuffer(content, contextio.Reader{Context: ctx, Source: file, OnRead: progress.noteBytes}, scratch)
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
