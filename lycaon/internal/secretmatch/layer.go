package secretmatch

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Layer is a bundled catalog or a host overlay.
type Layer struct {
	// bundled is the embedded catalog root.
	bundled config.Rel
	// dir is the host overlay directory.
	dir string
}

// Bundled is the stock catalog that ships in the binary.
func Bundled() Layer { return Layer{bundled: config.SecretPatternsDir} }

// Dir returns a host overlay layer. Missing directories are empty layers.
func Dir(path string) Layer { return Layer{dir: strings.TrimSpace(path)} }

func (l Layer) isBundled() bool { return l.bundled != "" }

// list returns the catalog filenames in this layer, sorted.
func (l Layer) list() ([]string, error) {
	var ents []fs.DirEntry
	var err error
	if l.isBundled() {
		ents, err = config.List(l.bundled)
	} else {
		if l.dir == "" {
			return nil, nil
		}
		ents, err = os.ReadDir(l.dir)
	}
	if err != nil {
		if !l.isBundled() && os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("secretmatch: read %s: %w", l, err)
	}
	out := make([]string, 0, len(ents))
	for _, ent := range ents {
		name := ent.Name()
		if ent.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// read returns one catalog file plus a display path for diagnostics.
func (l Layer) read(name string) ([]byte, string, error) {
	if l.isBundled() {
		rel := l.bundled.Join(name)
		raw, err := config.Read(rel)
		if err != nil {
			return nil, "", fmt.Errorf("secretmatch: read %s: %w", rel, err)
		}
		return raw, rel.String(), nil
	}
	path := filepath.Join(l.dir, name)
	// #nosec G304 -- path is a catalog child returned by ReadDir.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("secretmatch: read %s: %w", path, err)
	}
	return raw, path, nil
}

func (l Layer) String() string {
	if l.isBundled() {
		return l.bundled.String()
	}
	return l.dir
}
