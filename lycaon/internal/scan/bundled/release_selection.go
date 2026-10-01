package bundled

import (
	"context"
	"fmt"
	"path/filepath"
)

// SelectReleaseArtifact rechecks the pinned archive when a build consumes a cached directory.
func SelectReleaseArtifact(ctx context.Context, source *Manifest, directory, goos, goarch string) (*Manifest, error) {
	pin, err := source.ReleaseForPlatform(goos, goarch)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	archive, err := openPinnedArchive(ctx, filepath.Join(filepath.Dir(absolute), pin.SHA256+".tar.gz"), pin)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archive.file.Close() }()
	members, err := archive.scan(ctx, "")
	if err != nil {
		return nil, err
	}
	resolved, err := admitReleaseDirectory(source, absolute, goos, goarch, members)
	if err != nil {
		return nil, err
	}
	return resolved.Manifest, nil
}

func (m *Manifest) verifyReleaseSelection(ctx context.Context) error {
	if !m.releaseAdmitted {
		return fmt.Errorf("release selection requires pinned archive admission")
	}
	_, err := SelectReleaseArtifact(ctx, m, m.artifactDirectory, m.identity.GOOS, m.identity.GOARCH)
	if err != nil {
		return err
	}
	return nil
}
