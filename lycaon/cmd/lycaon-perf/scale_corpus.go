package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/dotversion"
)

// corpusRoot holds one released store per version, release candidates beside them.
var corpusRoot = filepath.Join("testdata", "upgrade-corpus")

// newestReleasedCorpus is the last released version's corpus: the store an
// upgrade starts from. Release candidates carry a hyphen and are skipped.
func newestReleasedCorpus(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	newest := ""
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.Contains(name, "-") {
			continue
		}
		if newest == "" {
			newest = name
			continue
		}
		order, err := dotversion.Compare(name, newest)
		if err != nil {
			return "", fmt.Errorf("corpus %s: %w", name, err)
		}
		if order > 0 {
			newest = name
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no released corpus under %s", root)
	}
	return filepath.Join(root, newest), nil
}

type corpusManifest struct {
	RetainedBranchTrees []string `json:"retained_branch_trees"`
	Files               []struct {
		RelPath string      `json:"rel_path"`
		Kind    string      `json:"kind"`
		Mode    fs.FileMode `json:"mode"`
	} `json:"files"`
}

// materializeCorpus lays out a released corpus as that release left its data
// directory: the archive's retained files, then the store and app state.
func materializeCorpus(corpus, configDir string) error {
	archive, err := zip.OpenReader(filepath.Join(corpus, "backup.zip"))
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	var manifest corpusManifest
	if err := readZipJSON(&archive.Reader, "manifest.json", &manifest); err != nil {
		return err
	}
	for _, tree := range manifest.RetainedBranchTrees {
		target, err := corpusTarget(configDir, tree)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(target, 0o700); err != nil {
			return err
		}
	}
	for _, item := range manifest.Files {
		target, err := corpusTarget(configDir, item.RelPath)
		if err != nil {
			return err
		}
		body, err := readZip(&archive.Reader, item.RelPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if item.Kind == "symlink" {
			err = os.Symlink(string(body), target)
		} else {
			err = os.WriteFile(target, body, item.Mode.Perm())
		}
		if err != nil {
			return err
		}
	}
	if err := copyFile(filepath.Join(corpus, "store.db"), filepath.Join(configDir, "store.db")); err != nil {
		return err
	}
	state := filepath.Join(corpus, "app-state-v1")
	return filepath.WalkDir(state, func(source string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(corpus, source)
		if err != nil {
			return err
		}
		target := filepath.Join(configDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return copyFile(source, target)
	})
}

func corpusTarget(root, rel string) (string, error) {
	clean := path.Clean(rel)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("corpus path %q leaves the data directory", rel)
	}
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}

func readZip(archive *zip.Reader, name string) ([]byte, error) {
	file, err := archive.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

func readZipJSON(archive *zip.Reader, name string, into any) error {
	body, err := readZip(archive, name)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

func copyFile(source, target string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, body, 0o600)
}
