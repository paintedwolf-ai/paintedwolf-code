package configlayout

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

// NixLayout describes resources from the running package, never custody trust.
type NixLayout struct {
	Output   string
	Root     string
	Version  string
	NixStore string
}

type nixReceipt struct {
	FormatVersion int    `json:"format_version"`
	Output        string `json:"output"`
	NixStore      string `json:"nix_store"`
}

var runningNix = sync.OnceValues(func() (*NixLayout, error) {
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	executable, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	image, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	defer func() { _ = image.Close() }()
	return nixLayout(executable, image)
})

// RunningNix returns immutable layout facts tied to the running ELF descriptor.
// A malformed present receipt is an error, not a developer-layout fallback.
func RunningNix() (*NixLayout, error) { return runningNix() }

func nixLayout(executable string, image io.Reader) (*NixLayout, error) {
	root := filepath.Dir(filepath.Dir(executable))
	raw, err := os.ReadFile(filepath.Join(root, "nix-layout.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 16384 {
		return nil, errors.New("Nix layout receipt too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt nixReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("Nix layout has trailing data")
	}
	if receipt.FormatVersion != 1 || !filepath.IsAbs(receipt.Output) || filepath.Clean(receipt.Output) != receipt.Output || !filepath.IsAbs(receipt.NixStore) || filepath.Clean(receipt.NixStore) != receipt.NixStore || filepath.Join(receipt.Output, "lib", "painted-wolf-code") != root || filepath.Dir(executable) != filepath.Join(root, "bin") {
		return nil, errors.New("Nix layout does not describe the running executable")
	}
	manifest, err := pkgmanifest.Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	if manifest.InstallRoot != root || manifest.Package != "painted-wolf-code" {
		return nil, errors.New("Nix manifest does not describe the running generation")
	}
	expected, ok := manifest.Lookup(executable)
	if !ok {
		return nil, &pkgmanifest.UnlistedError{Path: executable}
	}
	hash := sha256.New()
	n, err := io.Copy(hash, image)
	if err != nil {
		return nil, err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if n != expected.Size || got != expected.SHA256 {
		return nil, &pkgmanifest.MismatchError{Path: executable, Want: expected.SHA256, Got: got}
	}
	return &NixLayout{Output: receipt.Output, Root: root, Version: manifest.Version, NixStore: receipt.NixStore}, nil
}
