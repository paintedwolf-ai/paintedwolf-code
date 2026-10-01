package projectstack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

const StackMaxDeps = 16

// Manifest ecosystems — the keys registry doc URL templates are looked up by
// (config/packs/painted-wolf/platform/host/stack-registry-docs.yaml).
const (
	EcosystemGo       = "go"
	EcosystemNPM      = "npm"
	EcosystemCargo    = "cargo"
	EcosystemPyPI     = "pypi"
	EcosystemRubyGems = "rubygems"
	EcosystemComposer = "composer"
	EcosystemMetaCPAN = "metacpan"
)

// Dep is one manifest dependency with the ecosystem that declared it.
type Dep struct {
	Name      string
	Ecosystem string
}

var gemLineRE = regexp.MustCompile(`^\s*gem\s+['"]([^'"]+)['"]`)
var cpanfileRequiresRE = regexp.MustCompile(`(?m)^\s*requires\s*(?:\(\s*)?['"]([^'"]+)['"]`)

// depsFromManifests extracts deduplicated dependencies from discovered
// manifest paths under root, bounded to StackMaxDeps.
func depsFromManifests(root string, relPaths []string) []Dep {
	seen := make(map[string]struct{})
	var out []Dep
	addFor := func(ecosystem string) func(string) {
		return func(name string) {
			name = strings.TrimSpace(name)
			if name == "" || len(out) >= StackMaxDeps {
				return
			}
			key := ecosystem + "\x00" + name
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
			out = append(out, Dep{Name: name, Ecosystem: ecosystem})
		}
	}
	for _, rel := range relPaths {
		base := filepath.Base(rel)
		data, err := os.ReadFile(manifestAbs(root, rel)) // #nosec G703 -- rel from SurveyWalk under user root
		if err != nil {
			continue
		}
		switch base {
		case "go.mod":
			parseGoMod(data, addFor(EcosystemGo))
		case "package.json":
			parsePackageJSON(data, addFor(EcosystemNPM))
		case "Cargo.toml":
			parseCargoToml(data, addFor(EcosystemCargo))
		case "pyproject.toml":
			parsePyProject(data, addFor(EcosystemPyPI))
		case "requirements.txt":
			parseRequirements(data, addFor(EcosystemPyPI))
		case "Gemfile":
			parseGemfile(data, addFor(EcosystemRubyGems))
		case "composer.json":
			parseComposerJSON(data, addFor(EcosystemComposer))
		case "cpanfile":
			parseCPANFile(data, addFor(EcosystemMetaCPAN))
		case "META.json":
			parseCPANMeta(data, false, addFor(EcosystemMetaCPAN))
		case "META.yml":
			parseCPANMeta(data, true, addFor(EcosystemMetaCPAN))
		}
	}
	return out
}

func parseGoMod(data []byte, add func(string)) {
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return
	}
	for _, req := range f.Require {
		if !req.Indirect {
			add(req.Mod.Path)
		}
	}
}

func parsePackageJSON(data []byte, add func(string)) {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		names := make([]string, 0, len(deps))
		for name := range deps {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add(name)
		}
	}
}

func parseCargoToml(data []byte, add func(string)) {
	var cargo struct {
		Dependencies map[string]any `toml:"dependencies"`
	}
	if toml.Unmarshal(data, &cargo) != nil {
		return
	}
	names := make([]string, 0, len(cargo.Dependencies))
	for name := range cargo.Dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add(name)
	}
}

func parsePyProject(data []byte, add func(string)) {
	var py struct {
		Project struct {
			Dependencies []string `toml:"dependencies"`
		} `toml:"project"`
	}
	if toml.Unmarshal(data, &py) != nil {
		return
	}
	for _, dep := range py.Project.Dependencies {
		add(splitRequirementName(dep))
	}
}

func parseRequirements(data []byte, add func(string)) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		add(splitRequirementName(line))
	}
}

func parseGemfile(data []byte, add func(string)) {
	for _, line := range strings.Split(string(data), "\n") {
		m := gemLineRE.FindStringSubmatch(line)
		if len(m) == 2 {
			add(m[1])
		}
	}
}

func parseComposerJSON(data []byte, add func(string)) {
	var pkg struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	for _, deps := range []map[string]string{pkg.Require, pkg.RequireDev} {
		names := make([]string, 0, len(deps))
		for name := range deps {
			if name == "php" {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add(name)
		}
	}
}

func parseCPANFile(data []byte, add func(string)) {
	for _, match := range cpanfileRequiresRE.FindAllSubmatch(data, -1) {
		if len(match) != 2 {
			continue
		}
		name := strings.TrimSpace(string(match[1]))
		if name != "" && !strings.EqualFold(name, "perl") {
			add(name)
		}
	}
}

type cpanMeta struct {
	Prereqs           map[string]map[string]map[string]any `json:"prereqs" yaml:"prereqs"`
	Requires          map[string]any                       `json:"requires" yaml:"requires"`
	BuildRequires     map[string]any                       `json:"build_requires" yaml:"build_requires"`
	ConfigureRequires map[string]any                       `json:"configure_requires" yaml:"configure_requires"`
}

func parseCPANMeta(data []byte, legacyYAML bool, add func(string)) {
	var meta cpanMeta
	var err error
	if legacyYAML {
		err = yaml.Unmarshal(data, &meta)
	} else {
		err = json.Unmarshal(data, &meta)
	}
	if err != nil {
		return
	}

	names := map[string]struct{}{}
	collect := func(deps map[string]any) {
		for name := range deps {
			name = strings.TrimSpace(name)
			if name != "" && !strings.EqualFold(name, "perl") {
				names[name] = struct{}{}
			}
		}
	}
	collect(meta.Requires)
	collect(meta.BuildRequires)
	collect(meta.ConfigureRequires)
	for _, phase := range meta.Prereqs {
		for relation, deps := range phase {
			if strings.EqualFold(relation, "requires") {
				collect(deps)
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		add(name)
	}
}

func splitRequirementName(req string) string {
	req = strings.TrimSpace(req)
	for _, sep := range []string{"[", "=", ">", "<", "~", "!", ";", " "} {
		if i := strings.Index(req, sep); i >= 0 {
			req = req[:i]
		}
	}
	return req
}
