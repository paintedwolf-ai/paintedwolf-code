package modelstore

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type offlineBundle interface {
	open(string, int64) (io.ReadCloser, error)
	Close() error
}

type bundleDirectory struct{ *os.Root }

func (d bundleDirectory) open(name string, size int64) (io.ReadCloser, error) {
	return openBundleFile(d.Root, name, size)
}

type tarMember struct{ offset, size int64 }
type bundleArchive struct {
	*os.File
	members map[string]tarMember
}

func (b bundleArchive) open(name string, size int64) (io.ReadCloser, error) {
	member, ok := b.members[name]
	if !ok || member.size > size {
		return nil, fmt.Errorf("modelstore: invalid archive member %s", name)
	}
	return io.NopCloser(io.NewSectionReader(b.File, member.offset, member.size)), nil
}

// Plain tar offsets allow signed files to be read in any archive order.
func indexBundleTar(ctx context.Context, file *os.File) (offlineBundle, error) {
	b := bundleArchive{File: file, members: make(map[string]tarMember)}
	tr := tar.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return b, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || !nameShape.MatchString(h.Name) || h.Size <= 0 || h.Size > MaxArtifactBytes || len(b.members) >= 66 {
			return nil, errors.New("modelstore: invalid model archive entry")
		}
		if _, exists := b.members[h.Name]; exists {
			return nil, errors.New("modelstore: duplicate archive entry")
		}
		for k := range h.PAXRecords {
			if strings.HasPrefix(k, "GNU.sparse.") {
				return nil, errors.New("modelstore: sparse payload is unsupported")
			}
		}
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		b.members[h.Name] = tarMember{offset, h.Size}
	}
}

func openOfflineBundle(ctx context.Context, path string) (offlineBundle, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		root, err := os.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		return bundleDirectory{root}, nil
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	f, openErr := openBundleFile(root, filepath.Base(path), 66*MaxArtifactBytes)
	if err = errors.Join(openErr, root.Close()); err != nil {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
		return nil, err
	}
	b, err := indexBundleTar(ctx, f)
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return b, nil
}

func offlineManifest(b offlineBundle) (SignedManifest, error) {
	f, err := b.open("manifest.json", maxBundleManifestBytes)
	if err != nil {
		return SignedManifest{}, err
	}
	signed, err := decodeBundleManifest(f)
	return signed, errors.Join(err, f.Close())
}

func (s *Store) ImportFromBundlePath(ctx context.Context, path string, target Slot, progress func(done, total int64)) (resultErr error) {
	lock, err := s.provisioningLock()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	b, err := openOfflineBundle(ctx, path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, b.Close()) }()
	signed, err := offlineManifest(b)
	if err != nil {
		return err
	}
	m := signed.Manifest
	if m.Model != target.Model || m.Device != target.Device {
		return errors.New("modelstore: offline bundle does not match selected model and device")
	}
	transfer := transferProgress{total: manifestSize(m), report: progress}
	return s.install(ctx, signed, false, func() error { return verifyOfflinePayloads(ctx, b, m) }, func(a Artifact) error {
		f, err := b.open(a.Name, a.Bytes)
		if err != nil {
			return err
		}
		var stageErr error
		reader := io.TeeReader(contextReader{ctx: ctx, reader: f}, &transfer)
		if s.reusableArtifact(ctx, a) {
			stageErr = verifyReader(reader, a)
		} else {
			stageErr = s.stage(reader, a)
		}
		return errors.Join(stageErr, f.Close())
	})
}

// BundleSlot reads the requested identity. Import verifies the signed manifest.
func BundleSlot(ctx context.Context, path string) (Slot, error) {
	b, err := openOfflineBundle(ctx, path)
	if err != nil {
		return Slot{}, err
	}
	signed, err := offlineManifest(b)
	return Slot{Model: signed.Manifest.Model, Device: signed.Manifest.Device}, errors.Join(err, b.Close())
}

func verifyOfflinePayloads(ctx context.Context, b offlineBundle, m Manifest) error {
	for _, a := range manifestFiles(m) {
		f, err := b.open(a.Name, a.Bytes)
		if err != nil {
			return err
		}
		if err := errors.Join(verifyReader(contextReader{ctx: ctx, reader: f}, a), f.Close()); err != nil {
			return err
		}
	}
	return nil
}
