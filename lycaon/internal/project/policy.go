package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// ErrPolicyDenied is returned when project open policy blocks a path.
var ErrPolicyDenied = errors.New("project path denied by policy")

// OpenPolicy configures which directories may be opened via the API.
type OpenPolicy struct {
	DenyPathPrefixes []string `yaml:"deny_path_prefixes"`
	RequireUnderHome bool     `yaml:"require_under_home"`
}

var (
	bundledOpenPolicyOnce sync.Once
	bundledOpenPolicy     OpenPolicy
	bundledOpenPolicyErr  error
	defaultOpenPolicy     OpenPolicy
)

// DefaultOpenPolicy panics if the bundled policy cannot be loaded.
func DefaultOpenPolicy() OpenPolicy {
	bundledOpenPolicyOnce.Do(func() {
		bundledOpenPolicy, bundledOpenPolicyErr = bundledOpenPolicyConfig()
	})
	if bundledOpenPolicyErr != nil {
		panic(bundledOpenPolicyErr)
	}
	return bundledOpenPolicy
}

func bundledOpenPolicyConfig() (OpenPolicy, error) {
	data, err := config.Read(config.ProjectPolicy)
	if err != nil {
		return OpenPolicy{}, fmt.Errorf("read project policy: %w", err)
	}
	return decodeOpenPolicy(data)
}

func decodeOpenPolicy(data []byte) (OpenPolicy, error) {
	var file struct {
		DenyPathPrefixes []string `yaml:"deny_path_prefixes"`
		RequireUnderHome *bool    `yaml:"require_under_home"`
	}
	if err := config.DecodeYAML(data, &file); err != nil {
		return OpenPolicy{}, fmt.Errorf("parse project policy: %w", err)
	}
	var p OpenPolicy
	if len(file.DenyPathPrefixes) > 0 {
		p.DenyPathPrefixes = expandDenyPrefixes(file.DenyPathPrefixes)
	}
	if file.RequireUnderHome != nil {
		p.RequireUnderHome = *file.RequireUnderHome
	}
	if len(p.DenyPathPrefixes) == 0 {
		return OpenPolicy{}, fmt.Errorf("project policy: deny_path_prefixes required")
	}
	return p, nil
}

func expandDenyPrefixes(prefixes []string) []string {
	home, _ := os.UserHomeDir()
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "~/") && home != "" {
			out = append(out, filepath.Join(home, p[2:]))
		} else if p == "~" && home != "" {
			out = append(out, home)
		} else if !filepath.IsAbs(p) && home != "" && !strings.HasPrefix(p, "~") {
			out = append(out, filepath.Join(home, p))
		} else {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

// Resolve deny prefixes too so filesystem aliases match resolved project roots.
func (p OpenPolicy) ValidateOpenPath(abs string) error {
	abs = filepath.Clean(abs)
	for _, deny := range p.DenyPathPrefixes {
		deny = resolveForPolicy(deny)
		if pathUnderPrefix(abs, deny) {
			return fmt.Errorf("%w: path matches denied prefix %q", ErrPolicyDenied, deny)
		}
	}
	if p.RequireUnderHome {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home = resolveForPolicy(home)
		if !pathUnderPrefix(abs, home) && abs != home {
			return fmt.Errorf("%w: path must be under user home directory", ErrPolicyDenied)
		}
	}
	return nil
}

// Keep nonexistent deny prefixes as cleaned paths.
func resolveForPolicy(p string) string {
	cleaned := filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(cleaned); err == nil {
		return r
	}
	return cleaned
}

// Fold case so filesystem case aliases cannot bypass denied roots.
func pathUnderPrefix(path, prefix string) bool {
	p := strings.ToLower(path)
	pre := strings.ToLower(prefix)
	if p == pre {
		return true
	}
	return strings.HasPrefix(p, pre+string(os.PathSeparator))
}

// SetDefaultOpenPolicy installs the path policy for attached project roots.
func SetDefaultOpenPolicy(p OpenPolicy) {
	defaultOpenPolicy = p
}

// TestOpenPolicy permits temporary project roots outside the home directory.
func TestOpenPolicy() OpenPolicy {
	p := DefaultOpenPolicy()
	p.RequireUnderHome = false
	return p
}
