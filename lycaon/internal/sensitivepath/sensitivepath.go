// Package sensitivepath loads filesystem locations that require review.
package sensitivepath

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
)

// Mode identifies the action direction matched by a location.
type Mode string

const (
	// ModeRead matches read actions only.
	ModeRead Mode = "read"
	// ModeWrite matches write actions only.
	ModeWrite Mode = "write"
	// ModeAny matches both, for locations dangerous in either direction.
	ModeAny Mode = "any"
)

func (m Mode) valid() bool {
	switch m {
	case ModeRead, ModeWrite, ModeAny:
		return true
	}
	return false
}

// covers reports whether an entry declared for m applies to an action in actual.
func (m Mode) covers(actual Mode) bool { return m == ModeAny || m == actual }

// Location is one catalog entry. Paths are prefixes; names are basename globs.
type Location struct {
	ID    string   `yaml:"id"`
	Title string   `yaml:"title"`
	Mode  Mode     `yaml:"mode"`
	Paths []string `yaml:"paths"`
	Names []string `yaml:"names"`
	// Protected entries ask even inside attached roots.
	Protected bool `yaml:"protected"`
}

// catalogVersion rejects overlays with unknown shapes.
const catalogVersion = 1

type catalogFile struct {
	Version   int        `yaml:"version"`
	Locations []Location `yaml:"locations"`
}

// Match is a catalog hit.
type Match struct {
	ID    string
	Title string
	// Pattern is the matching path prefix or basename glob.
	Pattern string
	// Protected carries the entry's ask-inside-roots flag.
	Protected bool
}

// Catalog is the merged, home-expanded location set.
type Catalog struct {
	prefixes []prefixEntry
	names    []nameEntry
}

type prefixEntry struct {
	loc    Location
	prefix string
}

type nameEntry struct {
	loc  Location
	glob string
}

// Layer is a bundled catalog or host directory.
type Layer struct {
	bundled config.Rel
	dir     string
}

// Bundled is the stock catalog compiled into the binary.
func Bundled() Layer { return Layer{bundled: config.AskTriggerPathsDir} }

// Dir is a user or project overlay. Missing directories are empty layers.
func Dir(path string) Layer { return Layer{dir: strings.TrimSpace(path)} }

// Load merges layers by id and expands home-relative paths.
func Load(layers ...Layer) (*Catalog, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("sensitivepath: home directory: %w", err)
	}
	merged := map[string]Location{}
	var order []string
	for _, layer := range layers {
		locs, err := layer.read()
		if err != nil {
			return nil, err
		}
		for _, loc := range locs {
			if _, seen := merged[loc.ID]; !seen {
				order = append(order, loc.ID)
			}
			merged[loc.ID] = loc
		}
	}

	cat := &Catalog{}
	for _, id := range order {
		loc := merged[id]
		for _, raw := range loc.Paths {
			if expanded := expand(raw, home); expanded != "" {
				cat.prefixes = append(cat.prefixes, prefixEntry{loc: loc, prefix: expanded})
			}
		}
		for _, glob := range loc.Names {
			if glob = strings.TrimSpace(glob); glob != "" {
				cat.names = append(cat.names, nameEntry{loc: loc, glob: glob})
			}
		}
	}
	// Longest prefix first, so the most specific statement about a path wins and
	// a card cites the entry that says the most.
	sort.SliceStable(cat.prefixes, func(i, j int) bool {
		return len(cat.prefixes[i].prefix) > len(cat.prefixes[j].prefix)
	})
	return cat, nil
}

func (l Layer) read() ([]Location, error) {
	var files map[string][]byte
	var err error
	if l.bundled != "" {
		files, err = readBundled(l.bundled)
	} else if l.dir != "" {
		files, err = readDir(l.dir)
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)

	var out []Location
	for _, name := range paths {
		var parsed catalogFile
		if err := config.DecodeYAML(files[name], &parsed); err != nil {
			return nil, fmt.Errorf("sensitivepath: %s: %w", name, err)
		}
		if parsed.Version != catalogVersion {
			return nil, fmt.Errorf("sensitivepath: %s: catalog version %d is not supported (want %d)",
				name, parsed.Version, catalogVersion)
		}
		for _, loc := range parsed.Locations {
			loc.ID = strings.TrimSpace(loc.ID)
			loc.Title = strings.TrimSpace(loc.Title)
			if loc.ID == "" || loc.Title == "" {
				return nil, fmt.Errorf("sensitivepath: %s: every location needs an id and a title", name)
			}
			if !loc.Mode.valid() {
				return nil, fmt.Errorf("sensitivepath: %s: %s: mode must be read, write, or any", name, loc.ID)
			}
			if len(loc.Paths) == 0 && len(loc.Names) == 0 {
				return nil, fmt.Errorf("sensitivepath: %s: %s: needs paths or names", name, loc.ID)
			}
			out = append(out, loc)
		}
	}
	return out, nil
}

func readBundled(rel config.Rel) (map[string][]byte, error) {
	entries, err := config.List(rel)
	if err != nil {
		return nil, fmt.Errorf("sensitivepath: bundled catalog: %w", err)
	}
	out := map[string][]byte{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		raw, err := config.Read(rel.Join(name))
		if err != nil {
			return nil, fmt.Errorf("sensitivepath: %s: %w", name, err)
		}
		out[name] = raw
	}
	return out, nil
}

func readDir(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sensitivepath: %s: %w", dir, err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		// #nosec G304 -- e.Name is a direct child returned by ReadDir.
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("sensitivepath: %s: %w", e.Name(), err)
		}
		out[e.Name()] = raw
	}
	return out, nil
}

// MatchCovering reports the nearest sensitive descendant of root.
func (c *Catalog) MatchCovering(root string, mode Mode) (Match, bool) {
	if c == nil {
		return Match{}, false
	}
	clean := fspath.CanonicalPath(root)
	if clean == "" || clean == "/" {
		return Match{}, false
	}
	for _, entry := range c.prefixes {
		if !entry.loc.Mode.covers(mode) {
			continue
		}
		if confine.PathStrictlyUnder(entry.prefix, clean) {
			return Match{ID: entry.loc.ID, Title: entry.loc.Title, Pattern: entry.prefix, Protected: entry.loc.Protected}, true
		}
	}
	return Match{}, false
}

// Match reports the most specific path or name entry covering path.
func (c *Catalog) Match(path string, mode Mode) (Match, bool) {
	if c == nil {
		return Match{}, false
	}
	clean := fspath.CanonicalPath(path)
	if clean == "" {
		return Match{}, false
	}
	for _, entry := range c.prefixes {
		if !entry.loc.Mode.covers(mode) {
			continue
		}
		if confine.PathAtOrUnder(clean, entry.prefix) {
			return Match{ID: entry.loc.ID, Title: entry.loc.Title, Pattern: entry.prefix, Protected: entry.loc.Protected}, true
		}
	}
	base := filepath.Base(clean)
	for _, entry := range c.names {
		if !entry.loc.Mode.covers(mode) {
			continue
		}
		if confine.GlobMatchesName(clean, entry.glob, base) {
			return Match{ID: entry.loc.ID, Title: entry.loc.Title, Pattern: entry.glob, Protected: entry.loc.Protected}, true
		}
	}
	return Match{}, false
}

// ClassifyResolved checks the target and its sensitive descendants.
func (c *Catalog) ClassifyResolved(path string, mode Mode) (Match, bool) {
	if c == nil {
		return Match{}, false
	}
	if m, ok := c.Match(path, mode); ok {
		return m, true
	}
	return c.MatchCovering(path, mode)
}

// Locations returns the merged entries, for Settings and readiness.
func (c *Catalog) Locations() []Location {
	if c == nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := []Location{}
	for _, e := range c.prefixes {
		if _, dup := seen[e.loc.ID]; !dup {
			seen[e.loc.ID] = struct{}{}
			out = append(out, e.loc)
		}
	}
	for _, e := range c.names {
		if _, dup := seen[e.loc.ID]; !dup {
			seen[e.loc.ID] = struct{}{}
			out = append(out, e.loc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ProtectedPathRoots returns protected expanded prefixes.
// Basename rules require concrete path matching.
func (c *Catalog) ProtectedPathRoots(mode Mode) []string {
	if c == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, entry := range c.prefixes {
		if !entry.loc.Protected || !entry.loc.Mode.covers(mode) {
			continue
		}
		if _, duplicate := seen[entry.prefix]; duplicate {
			continue
		}
		seen[entry.prefix] = struct{}{}
		out = append(out, entry.prefix)
	}
	sort.Strings(out)
	return out
}

func expand(raw, home string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return ""
	case raw == "~":
		return fspath.CanonicalPath(home)
	case strings.HasPrefix(raw, "~/"):
		return fspath.CanonicalPath(filepath.Join(home, raw[2:]))
	default:
		return fspath.CanonicalPath(raw)
	}
}
