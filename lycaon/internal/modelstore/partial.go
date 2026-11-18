package modelstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
)

func (s *Store) downloadArtifact(ctx context.Context, source modelBucket, a Artifact, progress *transferProgress) (resultErr error) {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	if err := root.MkdirAll("partials", 0700); err != nil {
		return err
	}
	name := filepath.Join("partials", a.Digest)
	f, err := openPartial(root, name)
	if errors.Is(err, os.ErrPermission) {
		info, statErr := root.Lstat(name)
		if statErr != nil || !privatePartial(info) || info.Mode().Perm() != 0400 {
			return errors.Join(err, statErr)
		}
		// A crash can leave a sealed partial awaiting its final rename.
		if err := s.publishPartial(ctx, root, name, a); err != nil {
			return err
		}
		progress.add(a.Bytes)
		return nil
	}
	if err != nil {
		return err
	}
	err = s.fillPartial(ctx, source, a, f, progress)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return s.publishPartial(ctx, root, name, a)
}

func (s *Store) publishPartial(ctx context.Context, root *os.Root, name string, a Artifact) error {
	if err := auditFile(ctx, root, name, a); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.Join(err, root.Remove(name))
	}
	target := s.artifactRel(a.Digest)
	if err := root.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if err := root.Chmod(name, 0400); err != nil {
		return err
	}
	if err := root.Rename(name, target); err != nil {
		return err
	}
	return syncPublicationDirectories(root, filepath.Dir(target), fseffect.SyncDirectory)
}

func (s *Store) fillPartial(ctx context.Context, source modelBucket, a Artifact, f *os.File, progress *transferProgress) (resultErr error) {
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if offset > a.Bytes {
		if err := f.Truncate(0); err != nil {
			return err
		}
		offset = 0
	}
	if offset == a.Bytes {
		progress.add(offset)
		return nil
	}
	body, start, err := source.openPayload(ctx, a, offset)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, body.Close()) }()
	if start != offset {
		if err := f.Truncate(start); err != nil {
			return err
		}
	}
	missing := a.Bytes - start
	if err := s.CheckSpace(SpaceRequirement{StagingBytes: missing}); err != nil {
		return err
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	progress.add(start)
	n, err := io.Copy(io.MultiWriter(f, progress), contextReader{ctx: ctx, reader: io.LimitReader(body, missing+1)})
	if err != nil {
		return err
	}
	if n != missing {
		return fmt.Errorf("modelstore: received %d of %d bytes", n, missing)
	}
	return fssync.File(f)
}
