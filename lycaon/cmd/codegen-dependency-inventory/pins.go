package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

// value is one labeled cell entry.
type value struct {
	label string
	text  string
}

// pinReader resolves pin sources against one checkout, caching parsed files.
type pinReader struct {
	repo  string
	locks map[string]map[string][]string
}

func newPinReader(repo string) *pinReader {
	return &pinReader{repo: repo, locks: map[string]map[string][]string{}}
}

func (p *pinReader) read(rel string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(p.repo, rel)) // #nosec G304 -- path declared in the dependency policy
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return data, nil
}

func (p *pinReader) resolve(ref sourceRef) (string, error) {
	switch ref.Kind {
	case "file":
		data, err := p.read(ref.File)
		return strings.TrimSpace(string(data)), err
	case "shell":
		return p.shellVar(ref.File, ref.Name)
	case "go-const":
		return p.goConst(ref.File, ref.Name)
	case "toml", "yaml", "json":
		return p.structured(ref)
	case "go-directive", "go-require":
		return p.goMod(ref)
	case "npm-lock":
		lock, err := p.bunLock(ref.File)
		if err != nil {
			return "", err
		}
		return one(lock[ref.Name], ref)
	case "cargo-lock":
		lock, err := p.cargoLock(ref.File)
		if err != nil {
			return "", err
		}
		return highest(lock[ref.Name], ref)
	}
	return "", fmt.Errorf("unsupported pin kind %q", ref.Kind)
}

func one(versions []string, ref sourceRef) (string, error) {
	if len(versions) == 0 {
		return "", fmt.Errorf("%s has no entry for %s", ref.File, ref.Name)
	}
	return versions[0], nil
}

func highest(versions []string, ref sourceRef) (string, error) {
	best, err := one(versions, ref)
	for _, v := range versions[min(1, len(versions)):] {
		if compareVersions(v, best) > 0 {
			best = v
		}
	}
	return best, err
}

func (p *pinReader) shellVar(rel, name string) (string, error) {
	data, err := p.read(rel)
	if err != nil {
		return "", err
	}
	quoted := regexp.QuoteMeta(name)
	// Matches NAME="v1" and the overridable NAME="${NAME:-v1}".
	pattern := regexp.MustCompile(`(?m)^\s*(?:export\s+)?` + quoted + `="?(?:\$\{` + quoted + `:-)?([^"}\s]+)`)
	m := pattern.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("%s: no assignment to %s", rel, name)
	}
	return string(m[1]), nil
}

func (p *pinReader) goConst(rel, name string) (string, error) {
	data, err := p.read(rel)
	if err != nil {
		return "", err
	}
	file, err := parser.ParseFile(token.NewFileSet(), rel, data, 0)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", rel, err)
	}
	var found string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, ident := range spec.Names {
			if ident.Name != name || i >= len(spec.Values) {
				continue
			}
			if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				found, _ = strconv.Unquote(lit.Value)
			}
		}
		return found == ""
	})
	if found == "" {
		return "", fmt.Errorf("%s: no string constant %s", rel, name)
	}
	return found, nil
}

func (p *pinReader) structured(ref sourceRef) (string, error) {
	data, err := p.read(ref.File)
	if err != nil {
		return "", err
	}
	var doc any
	switch ref.Kind {
	case "toml":
		err = toml.Unmarshal(data, &doc)
	case "yaml":
		err = yaml.Unmarshal(data, &doc)
	default:
		err = json.Unmarshal(data, &doc)
	}
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", ref.File, err)
	}
	node, err := walk(doc, ref.Key)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ref.File, err)
	}
	if ref.Count {
		list, ok := node.([]any)
		if !ok {
			return "", fmt.Errorf("%s: %s is not a list", ref.File, ref.Key)
		}
		return strconv.Itoa(len(list)), nil
	}
	return scalar(node, ref)
}

func walk(node any, key string) (any, error) {
	for _, seg := range strings.Split(key, ".") {
		switch t := node.(type) {
		case map[string]any:
			next, ok := t[seg]
			if !ok {
				return nil, fmt.Errorf("no key %q in %s", seg, key)
			}
			node = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return nil, fmt.Errorf("bad index %q in %s", seg, key)
			}
			node = t[i]
		default:
			return nil, fmt.Errorf("cannot descend into %q in %s", seg, key)
		}
	}
	return node, nil
}

func scalar(node any, ref sourceRef) (string, error) {
	switch t := node.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("%s: %s is not a scalar", ref.File, ref.Key)
}

func (p *pinReader) goMod(ref sourceRef) (string, error) {
	data, err := p.read(ref.File)
	if err != nil {
		return "", err
	}
	f, err := modfile.Parse(ref.File, data, nil)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", ref.File, err)
	}
	if ref.Kind == "go-directive" {
		if f.Go == nil {
			return "", fmt.Errorf("%s has no go directive", ref.File)
		}
		return f.Go.Version, nil
	}
	for _, r := range f.Require {
		if r.Mod.Path == ref.Name {
			return r.Mod.Version, nil
		}
	}
	return "", fmt.Errorf("%s does not require %s", ref.File, ref.Name)
}

// bunLock maps each hoisted package name to its resolved version.
func (p *pinReader) bunLock(rel string) (map[string][]string, error) {
	if lock, ok := p.locks[rel]; ok {
		return lock, nil
	}
	data, err := p.read(rel)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Packages map[string][]json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(stripTrailingCommas(data), &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	lock := map[string][]string{}
	for name, entry := range doc.Packages {
		if len(entry) == 0 {
			continue
		}
		var spec string
		if json.Unmarshal(entry[0], &spec) != nil {
			continue
		}
		if at := strings.LastIndex(spec, "@"); at > 0 {
			lock[name] = []string{spec[at+1:]}
		}
	}
	p.locks[rel] = lock
	return lock, nil
}

// stripTrailingCommas turns bun's JSONC lockfile into JSON.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && strings.ContainsRune(" \t\r\n", rune(data[j])) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// cargoLock maps each crate name to every locked version.
func (p *pinReader) cargoLock(rel string) (map[string][]string, error) {
	if lock, ok := p.locks[rel]; ok {
		return lock, nil
	}
	data, err := p.read(rel)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Package []struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	lock := map[string][]string{}
	for _, pkg := range doc.Package {
		lock[pkg.Name] = append(lock[pkg.Name], pkg.Version)
	}
	p.locks[rel] = lock
	return lock, nil
}
