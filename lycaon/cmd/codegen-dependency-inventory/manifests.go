package main

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"
)

// row is one rendered inventory entry.
type row struct {
	key        string // annotation key within its section
	name       string // markdown, with qualifiers
	title      string // markdown, bare
	dependency string // Dependabot dependency name; empty when Dependabot does not manage it
	group      int
	pins       []value
	pinNote    string
	upstream   []upstreamRef
	judgement
}

// upstreamRef names the snapshot entry that holds one upstream value.
type upstreamRef struct {
	key    string
	label  string
	source sourceRef
}

func packageUpstream(kind, name string) []upstreamRef {
	ecosystem := map[string]string{kindGoModule: "go", kindNPM: "npm", kindCrate: "cargo"}[kind]
	return []upstreamRef{{key: ecosystem + ":" + name, source: sourceRef{Kind: kind, Name: name}}}
}

func manifestRows(p *pinReader, m *manifestConfig) ([]row, error) {
	switch m.Kind {
	case manifestGoMod:
		return goModuleRows(p, m)
	case manifestBun:
		return bunRows(p, m)
	case manifestCargo:
		return cargoRows(p, m)
	}
	return nil, fmt.Errorf("unsupported manifest kind %q", m.Kind)
}

func goModuleRows(p *pinReader, m *manifestConfig) ([]row, error) {
	data, err := p.read(m.Path)
	if err != nil {
		return nil, err
	}
	f, err := modfile.Parse(m.Path, data, nil)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.Path, err)
	}
	replaced := map[string]string{}
	for _, r := range f.Replace {
		replaced[r.Old.Path] = strings.TrimSpace(r.New.Path + " " + r.New.Version)
	}
	var rows []row
	for _, r := range f.Require {
		if r.Indirect {
			continue
		}
		rw := row{
			key:        r.Mod.Path,
			name:       "`" + r.Mod.Path + "`",
			title:      "`" + r.Mod.Path + "`",
			dependency: r.Mod.Path,
			pins:       []value{{text: r.Mod.Version}},
			upstream:   packageUpstream(kindGoModule, r.Mod.Path),
		}
		if to, ok := replaced[r.Mod.Path]; ok {
			rw.pinNote = "→ `" + to + "`"
		}
		rows = append(rows, rw)
	}
	return sortRows(rows), nil
}

type packageJSON struct {
	Dependencies        map[string]string `json:"dependencies"`
	DevDependencies     map[string]string `json:"devDependencies"`
	Overrides           map[string]string `json:"overrides"`
	PatchedDependencies map[string]string `json:"patchedDependencies"`
}

func bunRows(p *pinReader, m *manifestConfig) ([]row, error) {
	data, err := p.read(m.Path)
	if err != nil {
		return nil, err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.Path, err)
	}
	lock, err := p.bunLock(m.Lock)
	if err != nil {
		return nil, err
	}
	patched := map[string]bool{}
	for spec := range pkg.PatchedDependencies {
		if at := strings.LastIndex(spec, "@"); at > 0 {
			patched[spec[:at]] = true
		}
	}
	var rows []row
	for _, table := range []struct {
		deps      map[string]string
		qualifier string
	}{{pkg.Dependencies, ""}, {pkg.DevDependencies, "dev"}} {
		for name := range table.deps {
			installed, err := one(lock[name], sourceRef{File: m.Lock, Name: name})
			if err != nil {
				return nil, err
			}
			rw := row{
				key:        name,
				name:       qualified(name, table.qualifier),
				title:      "`" + name + "`",
				dependency: name,
				pins:       []value{{text: installed}},
				upstream:   packageUpstream(kindNPM, name),
			}
			if patched[name] {
				rw.pinNote = "+ local patch"
			}
			rows = append(rows, rw)
		}
	}
	for name, floor := range pkg.Overrides {
		rows = append(rows, row{
			key:      "override:" + name,
			name:     qualified(name, "override"),
			title:    "`" + name + "`",
			pins:     []value{{text: floor}},
			upstream: packageUpstream(kindNPM, name),
		})
	}
	return sortRows(rows), nil
}

type cargoDependency struct {
	name       string
	req        string
	qualifiers []string
	normal     bool
}

var targetOS = regexp.MustCompile(`^cfg\((not\()?target_os = "(\w+)"\)?\)$`)

func cargoRows(p *pinReader, m *manifestConfig) ([]row, error) {
	data, err := p.read(m.Path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", m.Path, err)
	}
	deps := map[string]*cargoDependency{}
	collectCargoTables(deps, doc, "")
	if targets, ok := doc["target"].(map[string]any); ok {
		for cfg, table := range targets {
			if t, ok := table.(map[string]any); ok {
				collectCargoTables(deps, t, targetLabel(cfg))
			}
		}
	}
	lock, err := p.cargoLock(m.Lock)
	if err != nil {
		return nil, err
	}
	var rows []row
	for _, dep := range deps {
		rw := row{
			key:        dep.name,
			name:       qualified(dep.name, dep.qualifier()),
			title:      "`" + dep.name + "`",
			dependency: dep.name,
			upstream:   packageUpstream(kindCrate, dep.name),
		}
		var best string
		for _, v := range lock[dep.name] {
			if cargoMatches(dep.req, v) && (best == "" || compareVersions(v, best) > 0) {
				best = v
			}
		}
		if best == "" {
			return nil, fmt.Errorf("%s: no locked version of %s satisfies %q", m.Lock, dep.name, dep.req)
		}
		rw.pins = []value{{text: best}}
		rows = append(rows, rw)
	}
	return sortRows(rows), nil
}

func collectCargoTables(deps map[string]*cargoDependency, doc map[string]any, target string) {
	for table, kind := range map[string]string{"dependencies": "", "build-dependencies": "build", "dev-dependencies": "dev"} {
		entries, ok := doc[table].(map[string]any)
		if !ok {
			continue
		}
		qualifier := strings.TrimSpace(target + " " + kind)
		for key, spec := range entries {
			name, req := key, ""
			switch s := spec.(type) {
			case string:
				req = s
			case map[string]any:
				req, _ = s["version"].(string)
				if renamed, ok := s["package"].(string); ok {
					name = renamed
				}
			}
			dep, ok := deps[name]
			if !ok {
				dep = &cargoDependency{name: name, req: req}
				deps[name] = dep
			}
			if qualifier == "" {
				dep.normal = true
			} else {
				dep.qualifiers = append(dep.qualifiers, qualifier)
			}
		}
	}
}

func (d *cargoDependency) qualifier() string {
	if d.normal {
		return ""
	}
	sort.Strings(d.qualifiers)
	return strings.Join(d.qualifiers, ", ")
}

func targetLabel(cfg string) string {
	m := targetOS.FindStringSubmatch(cfg)
	if m == nil {
		return cfg
	}
	os := map[string]string{"macos": "macOS", "windows": "Windows", "linux": "Linux"}[m[2]]
	if os == "" {
		os = m[2]
	}
	if m[1] != "" {
		return "not " + os
	}
	return os
}

func qualified(name, qualifier string) string {
	if qualifier == "" {
		return "`" + name + "`"
	}
	return "`" + name + "` · " + qualifier
}

func sortRows(rows []row) []row {
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	return rows
}

// manifestDirectory is the Dependabot directory for a manifest path.
func manifestDirectory(manifestPath string) string {
	dir := path.Dir(manifestPath)
	if dir == "." {
		return "/"
	}
	return "/" + dir
}
