package modelstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
)

func (s *Store) downloadArtifact(ctx context.Context, client *http.Client, url string, a Artifact, progress *transferProgress) (resultErr error) {
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
	err = s.fillPartial(ctx, client, url, a, f, progress)
	if err = errors.Join(err, f.Close()); err != nil {
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

func (s *Store) fillPartial(ctx context.Context, client *http.Client, url string, a Artifact, f *os.File, progress *transferProgress) error {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	transferErr := s.copyResponse(ctx, resp, f, a.Bytes, offset, progress)
	return errors.Join(transferErr, resp.Body.Close())
}

func (s *Store) copyResponse(ctx context.Context, resp *http.Response, f *os.File, size, offset int64, progress *transferProgress) error {
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0
		if err := f.Truncate(0); err != nil {
			return err
		}
	case http.StatusPartialContent:
		if offset == 0 || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, size-1, size) {
			return errors.New("modelstore: inconsistent range response")
		}
	default:
		return fmt.Errorf("modelstore: payload HTTP %d", resp.StatusCode)
	}
	missing := size - offset
	if resp.ContentLength >= 0 && resp.ContentLength != missing {
		return errors.New("modelstore: payload length differs from manifest")
	}
	if err := s.CheckSpace(SpaceRequirement{StagingBytes: missing}); err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	progress.add(offset)
	n, err := io.Copy(io.MultiWriter(f, progress), contextReader{ctx: ctx, reader: io.LimitReader(resp.Body, missing+1)})
	if err != nil {
		return err
	}
	if n != missing {
		return fmt.Errorf("modelstore: received %d of %d bytes", n, missing)
	}
	return fssync.File(f)
}
