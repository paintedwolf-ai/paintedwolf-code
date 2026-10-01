package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// DefaultMaxStdinBytes caps literal stdin passed to the first pipeline stage.
const DefaultMaxStdinBytes = 1 << 20

// StdinSpec feeds the first pipeline stage's stdin.
type StdinSpec struct {
	Literal []byte
	From    *fseffect.Location
	// Reader feeds the process for as long as the caller writes; a resident
	// worker that serves many requests over one process reads from it.
	Reader io.ReadCloser
}

// OutputTarget is one resolved output file and its write mode.
type OutputTarget struct {
	Location fseffect.Location
	Append   bool
}

// RedirectSpec binds every file a plan writes or reads.
type RedirectSpec struct {
	// Commit reviews captured output before it replaces the destination.
	Commit func(context.Context, fseffect.Location, io.Reader, bool) error
	// Stdout and Stderr receive a copy of the call's output streams.
	Stdout *OutputTarget
	Stderr *OutputTarget
	// Files resolves each stage redirection path, as written, to its location.
	Files map[string]fseffect.Location
}

// ErrRedirectUnbound reports a stage redirection path with no resolved location.
var ErrRedirectUnbound = errors.New("redirection target is not bound to a location")

// Bind resolves one stage redirection path to its location.
func (spec *RedirectSpec) Bind(path string, loc fseffect.Location) {
	if spec.Files == nil {
		spec.Files = map[string]fseffect.Location{}
	}
	spec.Files[path] = loc
}

func (spec *RedirectSpec) location(path string) (fseffect.Location, error) {
	if spec != nil {
		if loc, ok := spec.Files[path]; ok {
			return loc, nil
		}
	}
	return fseffect.Location{}, fmt.Errorf("%w: %q", ErrRedirectUnbound, path)
}

var (
	// ErrInvalidEnvKey is returned when an inline env key fails validation.
	ErrInvalidEnvKey = errors.New("invalid env key")
	// ErrBlockedEnvKey is returned when an inline env key is scrub-critical.
	ErrBlockedEnvKey = errors.New("blocked env key")
	// ErrStdinTooLarge is returned when literal stdin exceeds DefaultMaxStdinBytes.
	ErrStdinTooLarge = errors.New("stdin exceeds size cap")
)

var inlineEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateInlineEnv applies the case-insensitive scrub rules.
func ValidateInlineEnv(env map[string]string) error {
	for key := range env {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("%w: empty key", ErrInvalidEnvKey)
		}
		if !inlineEnvKeyPattern.MatchString(key) {
			return fmt.Errorf("%w: %q", ErrInvalidEnvKey, key)
		}
		upper := strings.ToUpper(key)
		if _, blocked := blockedEnvKeys[upper]; blocked {
			return fmt.Errorf("%w: %s", ErrBlockedEnvKey, key)
		}
		if _, blocked := inlineBlockedEnvKeys[upper]; blocked {
			return fmt.Errorf("%w: %s", ErrBlockedEnvKey, key)
		}
		if hasBlockedPrefix(upper) {
			return fmt.Errorf("%w: %s", ErrBlockedEnvKey, key)
		}
	}
	return nil
}

func openStdin(spec *StdinSpec) (io.ReadCloser, error) {
	if spec == nil {
		return nil, nil
	}
	if len(spec.Literal) > 0 {
		if len(spec.Literal) > DefaultMaxStdinBytes {
			return nil, fmt.Errorf("%w at %d bytes", ErrStdinTooLarge, DefaultMaxStdinBytes)
		}
		return io.NopCloser(strings.NewReader(string(spec.Literal))), nil
	}
	if spec.From != nil {
		f, err := fseffect.OpenRead(*spec.From)
		if err != nil {
			return nil, fmt.Errorf("open stdin file: %w", err)
		}
		return f, nil
	}
	if spec.Reader != nil {
		return spec.Reader, nil
	}
	return nil, nil
}

func openRedirectFile(loc fseffect.Location, appendMode bool) (*os.File, error) {
	f, err := fseffect.OpenWrite(loc, appendMode, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open redirect file: %w", err)
	}
	return f, nil
}

func envSliceToMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, val, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func envMapToSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func buildProcessEnv(provided []string, inline map[string]string, pathExtra []string) ([]string, error) {
	base := appendToPath(resolveEnv(provided), pathExtra)
	if len(inline) == 0 {
		return base, nil
	}
	if err := ValidateInlineEnv(inline); err != nil {
		return nil, err
	}
	merged := envSliceToMap(base)
	for k, v := range inline {
		merged[k] = v
	}
	return envMapToSlice(merged), nil
}

// appendToPath preserves command precedence and removes duplicates.
func appendToPath(env []string, extra []string) []string {
	if len(extra) == 0 {
		return env
	}
	sep := string(filepath.ListSeparator)
	current := ""
	index := -1
	for i, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			current, index = value, i
			break
		}
	}
	present := map[string]struct{}{}
	for _, dir := range filepath.SplitList(current) {
		present[filepath.Clean(dir)] = struct{}{}
	}
	added := current
	for _, dir := range extra {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" || dir == "." || !filepath.IsAbs(dir) {
			continue
		}
		if _, duplicate := present[dir]; duplicate {
			continue
		}
		present[dir] = struct{}{}
		if added == "" {
			added = dir
			continue
		}
		added += sep + dir
	}
	if added == current {
		return env
	}
	out := append([]string(nil), env...)
	if index >= 0 {
		out[index] = "PATH=" + added
		return out
	}
	return append(out, "PATH="+added)
}
