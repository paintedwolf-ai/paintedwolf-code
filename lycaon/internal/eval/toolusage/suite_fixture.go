package toolusage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// snapshotFixture admits bounded regular files and rejects links.
func snapshotFixture(rootPath, destination string) (digest string, err error) {
	info, err := os.Lstat(rootPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fixture root is not a directory: %s", rootPath)
	}
	fixtureRoot, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, fixtureRoot.Close()) }()
	hash := sha256.New()
	var total int64
	err = filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fixture must contain only directories and regular files: %s", path)
		}
		total += info.Size()
		if info.Size() > 4<<20 || total > 64<<20 {
			return fmt.Errorf("fixture exceeds 4 MiB per file / 64 MiB total")
		}
		rel, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		body, err := readFixtureFile(fixtureRoot, rel)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(rel), len(body))
		_, _ = hash.Write(body)
		if destination != "" {
			_, err = fseffect.Replace(fseffect.ReplaceRequest{
				Location: fseffect.Location{Root: destination, Rel: rel}, Source: bytes.NewReader(body), Mode: info.Mode().Perm(),
			})
		}
		return err
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}

func unchangedFixture(source, project string) (err error) {
	projectRoot, err := os.OpenRoot(project)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, projectRoot.Close()) }()
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, sourceRoot.Close()) }()
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		original, err := readFixtureFile(sourceRoot, rel)
		if err != nil {
			return err
		}
		current, err := readFixtureFile(projectRoot, rel)
		if err != nil {
			return fmt.Errorf("original fixture file unavailable: %s: %w", rel, err)
		}
		if !bytes.Equal(original, current) {
			return fmt.Errorf("read-only task changed original file %s", rel)
		}
		return nil
	})
	return err
}

func readFixtureFile(root *os.Root, rel string) (body []byte, err error) {
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return io.ReadAll(io.LimitReader(file, (4<<20)+1))
}
