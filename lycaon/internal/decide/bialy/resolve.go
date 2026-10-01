// Package bialy runs Bialy as a supervised subprocess over line-delimited JSON.
package bialy

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide"
)

const (
	// EnvBinary overrides the engine executable for development checkouts.
	EnvBinary = "LYCAON_DECIDE_BINARY"
	// EnvDisabled switches the engine off; every decision then abstains.
	EnvDisabled = "LYCAON_DECIDE_DISABLED"
	// EnvDevice pins the inference device: auto, cpu, cuda, cuda:N, metal, metal:N.
	EnvDevice = "LYCAON_DECIDE_DEVICE"
	// EnvModelDir overrides the checkpoint directory.
	EnvModelDir = "LYCAON_DECIDE_MODEL_DIR"
	// EnvHeads names head files, as "turn-load=PATH,code-rank=PATH".
	EnvHeads = "LYCAON_DECIDE_HEADS"
	// binaryName is the engine executable beside the host executable.
	binaryName = "bialy"
	// headsDir holds the bundled head files under the engine root.
	headsDir = "decide/heads"
	// EnvMetallib overrides MLX's Metal library file.
	EnvMetallib = "LYCAON_DECIDE_METALLIB"
	// metallibRel is MLX's Metal library under the engine root.
	metallibRel = "decide/mlx.metallib"
	// modelsDir holds the bundled checkpoints under the engine root.
	modelsDir = "decide/models"
)

// Config selects the engine process and what it loads.
type Config struct {
	// Binary is an explicit executable path; empty resolves the bundled sibling.
	Binary string
	// Device is passed to the engine when set.
	Device string
	// ModelDir is the checkpoint directory; empty resolves the bundled checkpoint,
	// then the managed one.
	ModelDir string
	// ModelID names the checkpoint in receipts.
	ModelID string
	// Heads maps a head name to its file.
	Heads map[decide.Head]string
	// HeadMaxLen is the engine's token budget for a question head; 0 keeps the
	// checkpoint's own.
	HeadMaxLen int
	// Metallib is MLX's Metal library, passed to the engine when set; the engine
	// otherwise looks beside its own executable.
	Metallib string
	// Disabled abstains without resolving anything.
	Disabled bool
}

// ConfigFromEnvironment reads the development overrides over the bundled defaults.
func ConfigFromEnvironment() Config {
	cfg := Config{
		Binary:   strings.TrimSpace(os.Getenv(EnvBinary)),
		Device:   strings.TrimSpace(os.Getenv(EnvDevice)),
		ModelDir: strings.TrimSpace(os.Getenv(EnvModelDir)),
		Disabled: configdir.EnvTruthy(os.Getenv(EnvDisabled)),
		Heads:    bundledHeads(),
		Metallib: strings.TrimSpace(os.Getenv(EnvMetallib)),
	}
	if cfg.Metallib == "" {
		cfg.Metallib = bundledMetallib()
	}
	for head, path := range parseHeads(os.Getenv(EnvHeads)) {
		cfg.Heads[head] = path
	}
	if cfg.ModelDir == "" {
		cfg.ModelDir = bundledModelDir()
	}
	if cfg.ModelDir == "" {
		cfg.ModelDir = ManagedModelDir()
	}
	cfg.ModelID = ShippedModel.ID
	return cfg
}

// parseHeads reads "turn-load=PATH,code-rank=PATH"; unknown names are dropped.
func parseHeads(spec string) map[decide.Head]string {
	heads := map[decide.Head]string{}
	for entry := range strings.SplitSeq(spec, ",") {
		name, path, ok := strings.Cut(strings.TrimSpace(entry), "=")
		head := decide.Head(strings.TrimSpace(name))
		path = strings.TrimSpace(path)
		if !ok || path == "" || !slices.Contains(decide.Heads(), head) {
			continue
		}
		heads[head] = path
	}
	return heads
}

// bundledHeads lists the head files staged under the engine root, one per head name.
func bundledHeads() map[decide.Head]string {
	heads := map[decide.Head]string{}
	root := configlayout.EngineRoot()
	if root == "" {
		return heads
	}
	for _, head := range decide.Heads() {
		path := filepath.Join(root, filepath.FromSlash(headsDir), string(head)+".safetensors")
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			heads[head] = path
		}
	}
	return heads
}

// bundledMetallib is the Metal library staged under the engine root, or "" when none is.
func bundledMetallib() string {
	root := configlayout.EngineRoot()
	if root == "" {
		return ""
	}
	path := filepath.Join(root, filepath.FromSlash(metallibRel))
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		return path
	}
	return ""
}

// bundledModelDir is the shipped checkpoint staged under the engine root, or "" unless
// every pinned file and the completion marker are there.
func bundledModelDir() string {
	root := configlayout.EngineRoot()
	if root == "" {
		return ""
	}
	dir := filepath.Join(root, filepath.FromSlash(ShippedModel.BundledRel()))
	if !modelReady(dir) {
		return ""
	}
	return dir
}

// Resolve returns the engine executable to run, or "" when none is installed.
func (c Config) Resolve() string {
	if c.Disabled {
		return ""
	}
	if path := strings.TrimSpace(c.Binary); path != "" {
		if isExecutable(path) {
			return path
		}
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return bundledBinary(exe, runtime.GOOS)
}

// bundledBinary locates bialy beside the host executable: Contents/MacOS in
// the app bundle, the same directory elsewhere.
func bundledBinary(executable, goos string) string {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil {
		real = executable
	}
	name := binaryName
	if goos == "windows" {
		name += ".exe"
	}
	dir := filepath.Dir(real)
	if goos == "darwin" {
		if contents := configlayout.MacOSAppContents(real); contents != "" {
			dir = filepath.Join(contents, "MacOS")
		}
	}
	path := filepath.Join(dir, name)
	if !isExecutable(path) {
		return ""
	}
	return path
}

// Args builds the engine's serve command line.
func (c Config) Args() []string {
	args := []string{"serve", "--model", c.ModelDir}
	if c.ModelID != "" {
		args = append(args, "--model-id", c.ModelID)
	}
	if c.Device != "" {
		args = append(args, "--device", c.Device)
	}
	if c.HeadMaxLen > 0 {
		args = append(args, "--head-max-len", strconv.Itoa(c.HeadMaxLen))
	}
	if c.Metallib != "" {
		args = append(args, "--metallib", c.Metallib)
	}
	for _, head := range decide.Heads() {
		if path := c.Heads[head]; path != "" {
			args = append(args, "--head", string(head)+"="+path)
		}
	}
	return args
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || st.Mode()&0o111 != 0
}
