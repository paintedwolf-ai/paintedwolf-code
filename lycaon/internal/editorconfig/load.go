package editorconfig

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// FileName is the configuration file searched in each ancestor directory.
const FileName = ".editorconfig"

// MaxFileBytes bounds one configuration file.
const MaxFileBytes = 256 << 10

// ErrFileTooLarge marks a configuration file over MaxFileBytes.
var ErrFileTooLarge = errors.New("editorconfig file exceeds size limit")

// Load resolves properties for a root-relative path, walking from its directory
// up to root and stopping early at `root = true`.
func Load(root, rel string) (Properties, error) {
	configs, err := loadChain(root, rel)
	if err != nil {
		return Properties{}, err
	}
	return Resolve(rel, configs), nil
}

// SearchDirs lists a path's ancestor directories from nearest to root ("").
func SearchDirs(rel string) []string {
	clean := strings.TrimPrefix(filepath.ToSlash(rel), "./")
	dir := path.Dir(clean)
	var dirs []string
	for dir != "." && dir != "/" && dir != "" {
		dirs = append(dirs, dir)
		dir = path.Dir(dir)
	}
	return append(dirs, "")
}

func loadChain(root, rel string) ([]File, error) {
	var nearestFirst []File
	for _, dir := range SearchDirs(rel) {
		body, found, err := readConfig(root, path.Join(dir, FileName))
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		f := Parse(dir, body)
		nearestFirst = append(nearestFirst, f)
		if f.Root {
			break
		}
	}
	farthestFirst := make([]File, len(nearestFirst))
	for i, f := range nearestFirst {
		farthestFirst[len(nearestFirst)-1-i] = f
	}
	return farthestFirst, nil
}

func readConfig(root, rel string) (string, bool, error) {
	f, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: filepath.FromSlash(rel)})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", rel, err)
	}
	if len(body) > MaxFileBytes {
		return "", false, fmt.Errorf("%w: %s", ErrFileTooLarge, rel)
	}
	return string(body), true, nil
}
