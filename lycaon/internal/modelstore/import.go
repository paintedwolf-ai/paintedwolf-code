package modelstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/filelock"
)

var ErrRevisionInUse = errors.New("modelstore: revision is in use")

// Import retains staged payloads until the authenticated revision is activated.
func (s *Store) Import(ctx context.Context, signed SignedManifest, source func(context.Context, Artifact) (io.ReadCloser, error)) error {
	return s.importRevision(ctx, signed, source, false)
}

// Repair replaces damaged files after every reader has released the revision.
func (s *Store) Repair(ctx context.Context, signed SignedManifest, source func(context.Context, Artifact) (io.ReadCloser, error)) error {
	return s.importRevision(ctx, signed, source, true)
}

func (s *Store) importRevision(ctx context.Context, signed SignedManifest, source func(context.Context, Artifact) (io.ReadCloser, error), repair bool) error {
	if err := validate(signed.Manifest); err != nil {
		return err
	}
	if s.signer == nil || source == nil {
		return errors.New("modelstore: import requires an authority and artifact source")
	}
	if err := s.signer.Verify(signed); err != nil {
		return err
	}
	pins, err := s.pinImport(signed.Manifest, repair)
	if err != nil {
		return err
	}
	defer func() {
		for _, f := range pins {
			_ = f.Close()
		}
	}()
	for _, a := range manifestFiles(signed.Manifest) {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader, err := source(ctx, a)
		if err != nil {
			return err
		}
		if reader == nil {
			return errors.New("modelstore: artifact source returned no reader")
		}
		stageErr := s.Stage(contextReader{ctx: ctx, reader: reader}, a)
		closeErr := reader.Close()
		if err := errors.Join(stageErr, closeErr); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.activate(signed, repair)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *Store) artifactLock(digest string) (*os.File, error) {
	return filelock.Open(filepath.Join(s.root, "locks", "artifacts", digest+".lock"))
}

func (s *Store) pinImport(m Manifest, repair bool) ([]*os.File, error) {
	lock, err := s.lockJournal()
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	var pins []*os.File
	if repair {
		f, err := s.revisionLock(m)
		if err != nil {
			return nil, err
		}
		held, err := filelock.TryExclusive(f)
		if err != nil || !held {
			f.Close()
			if err != nil {
				return nil, err
			}
			return nil, ErrRevisionInUse
		}
		pins = append(pins, f)
	}
	for _, a := range manifestFiles(m) {
		f, err := s.artifactLock(a.Digest)
		if err == nil {
			pins = append(pins, f)
			var held bool
			held, err = filelock.TryShared(f)
			if err == nil && !held {
				err = fmt.Errorf("modelstore: artifact %s is being collected", a.Digest)
			}
		}
		if err != nil {
			for _, pin := range pins {
				_ = pin.Close()
			}
			return nil, err
		}
	}
	return pins, nil
}
