package modelstore

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fssync"
)

type fileSeal struct {
	Size     int64 `json:"size"`
	Modified int64 `json:"modified"`
}

type revisionSeal struct {
	Manifest Manifest            `json:"manifest"`
	Files    map[string]fileSeal `json:"files"`
}

func (s *Store) revisionRel(m Manifest) string {
	return filepath.Join("revisions", m.Model, m.Device, m.Revision)
}

func (s *Store) RevisionDir(m Manifest) string {
	return filepath.Join(s.root, s.revisionRel(m))
}

func sameManifest(a, b Manifest) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func manifestFiles(m Manifest) map[string]Artifact {
	files := map[string]Artifact{artifactFileName(m.Artifact, true): m.Artifact}
	for _, a := range m.Files {
		files[artifactFileName(a, false)] = a
	}
	return files
}

// materializeRevision publishes a complete directory under the journal lock.
func (s *Store) materializeRevision(m Manifest) (string, error) {
	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	rel := s.revisionRel(m)
	if _, err := root.Lstat(rel); err == nil {
		return s.checkRevision(m)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return "", err
	}
	tmp := rel + ".staging-" + rand.Text()
	if err := root.Mkdir(tmp, 0o700); err != nil {
		return "", err
	}
	defer root.RemoveAll(tmp)
	seal := revisionSeal{Manifest: m, Files: make(map[string]fileSeal)}
	for name, a := range manifestFiles(m) {
		src := s.artifactRel(a.Digest)
		info, err := root.Lstat(src)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() != a.Bytes {
			return "", fmt.Errorf("modelstore: invalid artifact %s", a.Digest)
		}
		if err := root.Link(src, filepath.Join(tmp, name)); err != nil {
			return "", err
		}
		seal.Files[name] = fileSeal{Size: info.Size(), Modified: info.ModTime().UnixNano()}
	}
	f, err := root.OpenFile(filepath.Join(tmp, ".manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return "", err
	}
	writeErr := json.NewEncoder(f).Encode(seal)
	if writeErr == nil {
		writeErr = fssync.File(f)
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := syncRootDir(root, tmp); err != nil {
		return "", err
	}
	if err := root.Rename(tmp, rel); err != nil {
		return "", err
	}
	if err := syncRootDir(root, filepath.Dir(rel)); err != nil {
		return "", err
	}
	return filepath.Join(s.root, rel), nil
}

// checkRevision reads metadata only; payload hashes are checked at installation and audit.
func (s *Store) checkRevision(m Manifest) (string, error) {
	root, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	rel := s.revisionRel(m)
	f, err := root.Open(filepath.Join(rel, ".manifest.json"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	var seal revisionSeal
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&seal); err != nil {
		return "", err
	}
	if !sameManifest(seal.Manifest, m) {
		return "", fmt.Errorf("modelstore: revision identity collision for %s", rel)
	}
	files := manifestFiles(m)
	if len(files) != len(seal.Files) {
		return "", fmt.Errorf("modelstore: incomplete revision seal")
	}
	for name, a := range files {
		info, err := root.Lstat(filepath.Join(rel, name))
		if err != nil {
			return "", err
		}
		expected, ok := seal.Files[name]
		if !ok || !info.Mode().IsRegular() || info.Size() != a.Bytes ||
			info.Size() != expected.Size || info.ModTime().UnixNano() != expected.Modified {
			return "", fmt.Errorf("modelstore: artifact %s changed after verification", name)
		}
	}
	return filepath.Join(s.root, rel), nil
}

func syncRootDir(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return fssync.File(f)
}
