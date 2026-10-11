package projectsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/contextio"
	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Transfer evidence describes the bytes written into an unpublished tree.
type sourceTreeTransfer struct {
	rootContent []byte
	rootSHA     string
	ctx         context.Context
	digest      *sourceTreeDigest
	capture     *sourceRecoveryCapture
	progress    *sourceWorkProgress
}

func (t *sourceTreeTransfer) copyEntry(source *os.Root, from string, destination *os.Root, to, rel string) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	info, err := source.Lstat(from)
	if err != nil {
		return err
	}
	entry := sourceledger.RecoveryEntry{Path: rel, Mode: uint32(info.Mode())}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		entry.Link, err = source.Readlink(from)
		if err == nil {
			err = destination.Symlink(entry.Link, to)
		}
	case info.IsDir():
		err = destination.Mkdir(to, 0o700)
	case info.Mode().IsRegular():
		entry, err = t.copyFile(source, from, destination, to, info, entry)
	default:
		err = fmt.Errorf("cannot copy special file %s", from)
	}
	if err != nil {
		return err
	}
	if t.capture != nil && !info.Mode().IsRegular() {
		entry, err = t.capture.append(t.ctx, entry, source, from, nil)
		if err != nil {
			return err
		}
	}
	if t.capture == nil {
		if err := t.digest.append(entry); err != nil {
			return err
		}
		t.progress.entry()
	}
	if info.IsDir() {
		return t.copyDirectory(source, from, destination, to, rel, info)
	}
	return nil
}

func (t *sourceTreeTransfer) copyDirectory(source *os.Root, from string, destination *os.Root, to, rel string, info os.FileInfo) error {
	dir, err := source.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	opened, err := dir.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return ErrSourceMutationDiverged
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := t.copyEntry(source, filepath.Join(from, name), destination, filepath.Join(to, name), filepath.ToSlash(filepath.Join(rel, name))); err != nil {
			return err
		}
	}
	held, err := destination.Open(to)
	if err != nil {
		return err
	}
	syncErr := held.Chmod(info.Mode())
	if syncErr == nil {
		syncErr = fseffect.SyncDirectory(destination, to)
	}
	return errors.Join(syncErr, held.Close())
}

func (t *sourceTreeTransfer) copyFile(source *os.Root, from string, destination *os.Root, to string, info os.FileInfo, entry sourceledger.RecoveryEntry) (sourceledger.RecoveryEntry, error) {
	in, err := source.Open(from)
	if err != nil {
		return entry, err
	}
	defer func() { _ = in.Close() }()
	opened, err := in.Stat()
	if err != nil {
		return entry, err
	}
	if !sameSourceFileVersion(info, opened) {
		return entry, ErrSourceMutationDiverged
	}
	cloned, err := fileclone.CloneInto(in, destination, to)
	if err != nil {
		return entry, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if cloned {
		flags = os.O_RDONLY
	}
	out, err := destination.OpenFile(to, flags, 0o600)
	if err != nil {
		return entry, err
	}
	copyErr := out.Chmod(info.Mode())
	if copyErr == nil {
		entry, copyErr = t.transferFileBytes(source, from, destination, to, in, out, cloned, entry)
	}
	if copyErr == nil && !cloned {
		// Writing can clear permission bits on the destination.
		copyErr = out.Chmod(info.Mode())
	}
	if copyErr == nil {
		copyErr = fssync.File(out)
	}
	closeErr := out.Close()
	after, statErr := in.Stat()
	if copyErr == nil && statErr == nil && !sameSourceFileVersion(opened, after) {
		copyErr = ErrSourceMutationDiverged
	}
	return entry, errors.Join(copyErr, closeErr, statErr)
}

func (t *sourceTreeTransfer) transferFileBytes(source *os.Root, from string, destination *os.Root, to string, in, out *os.File, cloned bool, entry sourceledger.RecoveryEntry) (sourceledger.RecoveryEntry, error) {
	if t.capture != nil {
		if cloned {
			return t.capture.append(t.ctx, entry, destination, to, nil)
		}
		return t.capture.append(t.ctx, entry, source, from, out)
	}
	digest := sha256.New()
	var content bytes.Buffer
	outputs := []io.Writer{digest}
	reader := in
	if cloned {
		reader = out
	} else {
		outputs = append(outputs, out)
	}
	info, err := reader.Stat()
	if err != nil {
		return entry, err
	}
	retain := entry.Path == "." && info.Size() <= sourceledger.MaxRevisionContentBytes
	if retain {
		outputs = append(outputs, sourceRevisionBuffer{buffer: &content})
	}
	count, err := io.Copy(io.MultiWriter(outputs...), contextio.Reader{Context: t.ctx, Source: reader, OnRead: t.progress.noteBytes})
	entry.SHA = hex.EncodeToString(digest.Sum(nil))
	if entry.Path == "." && err == nil {
		t.rootSHA = entry.SHA
		if retain && count <= sourceledger.MaxRevisionContentBytes {
			t.rootContent = append([]byte{}, content.Bytes()...)
		}
	}
	return entry, err
}

// Keep a bounded revision preview even if a file grows during its transfer.
type sourceRevisionBuffer struct{ buffer *bytes.Buffer }

func (w sourceRevisionBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := sourceledger.MaxRevisionContentBytes - w.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.buffer.Write(p)
	}
	return n, nil
}
