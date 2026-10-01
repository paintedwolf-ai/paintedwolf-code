package extpacks

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Source preserves whether pack bytes are bundled or on disk.
type Source struct {
	rel config.Rel // bundled
	dir string     // on disk
}

// Bundled addresses shipped content inside the binary.
func Bundled(rel config.Rel) Source { return Source{rel: rel} }

// OnDisk addresses a pack directory on the host.
func OnDisk(dir string) Source { return Source{dir: strings.TrimSpace(dir)} }

// IsBundled reports whether s is shipped content.
func (s Source) IsBundled() bool { return s.dir == "" }

// Empty reports whether s addresses nothing.
func (s Source) Empty() bool { return s.dir == "" && s.rel == "" }

// Join extends s with further path segments, preserving which kind it is.
func (s Source) Join(parts ...string) Source {
	if len(parts) == 0 {
		return s
	}
	if s.IsBundled() {
		return Source{rel: s.rel.Join(parts...)}
	}
	return Source{dir: filepath.Join(append([]string{s.dir}, parts...)...)}
}

// Read expands overlay-directory placeholders in bundled and on-disk content.
func (s Source) Read() ([]byte, error) {
	if s.IsBundled() {
		return config.Read(s.rel)
	}
	data, err := os.ReadFile(s.dir) // #nosec G304 -- pack content under a user-chosen root
	if err != nil {
		return nil, err
	}
	return config.ExpandOverlayDir(data), nil
}

// Stat describes s.
func (s Source) Stat() (fs.FileInfo, error) {
	if s.IsBundled() {
		return config.Info(s.rel)
	}
	return os.Stat(s.dir)
}

// IsDir reports whether s exists and is a directory.
func (s Source) IsDir() bool {
	fi, err := s.Stat()
	return err == nil && fi.IsDir()
}

// List returns the entries directly under s, sorted by name.
func (s Source) List() ([]fs.DirEntry, error) {
	if s.IsBundled() {
		return config.List(s.rel)
	}
	return os.ReadDir(s.dir)
}

// Walk preserves the source kind for each entry.
func (s Source) Walk(fn func(Source, fs.DirEntry, error) error) error {
	if s.IsBundled() {
		return config.Walk(s.rel, func(rel config.Rel, d fs.DirEntry, err error) error {
			return fn(Source{rel: rel}, d, err)
		})
	}
	return filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		return fn(Source{dir: p}, d, err)
	})
}

// RelTo returns the slash path of s beneath root, for deriving unit ids.
func (s Source) RelTo(root Source) string {
	if s.IsBundled() != root.IsBundled() {
		return ""
	}
	if s.IsBundled() {
		rel := strings.TrimPrefix(s.rel.String(), root.rel.String())
		return strings.TrimPrefix(rel, "/")
	}
	rel, err := filepath.Rel(root.dir, s.dir)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// String renders s for ids, diagnostics, and logs.
func (s Source) String() string {
	if s.IsBundled() {
		return s.rel.String()
	}
	return s.dir
}

// FS returns a filesystem and its source root.
func (s Source) FS() (fs.FS, string) {
	if s.IsBundled() {
		return config.Source(), s.rel.String()
	}
	return os.DirFS(s.dir), "."
}

// MarshalJSON emits the source address.
func (s Source) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// Parent is the containing directory of s, preserving which kind it is.
func (s Source) Parent() Source {
	if s.IsBundled() {
		return Source{rel: s.rel.Dir()}
	}
	return Source{dir: filepath.Dir(s.dir)}
}

// Base returns the final source path element.
func (s Source) Base() string {
	if s.IsBundled() {
		return s.rel.Base()
	}
	return filepath.Base(s.dir)
}
