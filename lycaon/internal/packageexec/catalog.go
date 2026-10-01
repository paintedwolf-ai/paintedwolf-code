package packageexec

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/pkgregistry"
)

type catalogFile struct {
	Version  int       `yaml:"version"`
	Managers []manager `yaml:"managers"`
	Wrappers []wrapper `yaml:"wrappers"`
}

type wrapper struct {
	Executable   string   `yaml:"executable"`
	Mode         string   `yaml:"mode"`
	CommandFlags []string `yaml:"command_flags"`
	ValueFlags   []string `yaml:"value_flags"`
}

type manager struct {
	ID     string `yaml:"id"`
	System string `yaml:"system"`
	// Registry names the manager's entry in the public registry catalog.
	Registry string `yaml:"registry"`
	// DownloadHosts are general-purpose sites only a package action may reach.
	DownloadHosts []string      `yaml:"download_hosts"`
	Commands      []commandRule `yaml:"commands"`
	// registryHosts is resolved from Registry at load.
	registryHosts []string
}

type commandRule struct {
	Executable    string    `yaml:"executable"`
	Verbs         []string  `yaml:"verbs"`
	Operation     Operation `yaml:"operation"`
	PackageMode   string    `yaml:"package_mode"`
	PackageFlags  []string  `yaml:"package_flags"`
	RegistryFlags []string  `yaml:"registry_flags"`
	VersionFlags  []string  `yaml:"version_flags"`
	ValueFlags    []string  `yaml:"value_flags"`
}

type catalog struct {
	byExecutable map[string][]compiledRule
	wrappers     map[string]wrapper
}

type compiledRule struct {
	manager manager
	rule    commandRule
}

func loadCatalog() (*catalog, error) {
	registries, err := pkgregistry.Bundled()
	if err != nil {
		return nil, err
	}
	raw, err := config.Read(config.PackageExecutionManagers)
	if err != nil {
		return nil, fmt.Errorf("package execution catalog: %w", err)
	}
	var parsed catalogFile
	if err := config.DecodeYAML(raw, &parsed); err != nil {
		return nil, fmt.Errorf("package execution catalog: %w", err)
	}
	if parsed.Version != 1 || len(parsed.Managers) == 0 {
		return nil, fmt.Errorf("package execution catalog: unsupported or empty version")
	}
	out := &catalog{byExecutable: map[string][]compiledRule{}, wrappers: map[string]wrapper{}}
	seenManager := map[string]struct{}{}
	for i := range parsed.Managers {
		m := parsed.Managers[i]
		m.ID = strings.TrimSpace(m.ID)
		m.System = strings.ToUpper(strings.TrimSpace(m.System))
		m.Registry = strings.TrimSpace(m.Registry)
		m.DownloadHosts = normalizedStrings(m.DownloadHosts)
		if m.ID == "" || len(m.Commands) == 0 {
			return nil, fmt.Errorf("package execution catalog: every manager needs id and commands")
		}
		if _, duplicate := seenManager[m.ID]; duplicate {
			return nil, fmt.Errorf("package execution catalog: duplicate manager %q", m.ID)
		}
		seenManager[m.ID] = struct{}{}
		registry, ok := registries.ByID(m.Registry)
		if !ok {
			return nil, fmt.Errorf("package execution catalog: manager %q names unknown registry %q", m.ID, m.Registry)
		}
		m.registryHosts = registry.Hosts
		for _, host := range m.DownloadHosts {
			if owner, public := registries.ForHost(host); public {
				return nil, fmt.Errorf("package execution catalog: %s lists %q as a download host, but it serves the %s registry", m.ID, host, owner.ID)
			}
		}
		for j := range m.Commands {
			r := m.Commands[j]
			r.Executable = strings.TrimSpace(r.Executable)
			r.Verbs = trimmedStrings(r.Verbs)
			r.PackageFlags = trimmedStrings(r.PackageFlags)
			r.RegistryFlags = trimmedStrings(r.RegistryFlags)
			r.VersionFlags = trimmedStrings(r.VersionFlags)
			r.ValueFlags = trimmedStrings(r.ValueFlags)
			if r.Executable == "" || r.Operation == "" || !validPackageMode(r.PackageMode) {
				return nil, fmt.Errorf("package execution catalog: %s command %d is incomplete", m.ID, j)
			}
			out.byExecutable[r.Executable] = append(out.byExecutable[r.Executable], compiledRule{manager: m, rule: r})
		}
	}
	for i := range parsed.Wrappers {
		w := parsed.Wrappers[i]
		w.Executable = strings.TrimSpace(w.Executable)
		w.CommandFlags = trimmedStrings(w.CommandFlags)
		w.ValueFlags = trimmedStrings(w.ValueFlags)
		if w.Executable == "" || !validWrapperMode(w.Mode) {
			return nil, fmt.Errorf("package execution catalog: wrapper %d is incomplete", i)
		}
		if _, duplicate := out.wrappers[w.Executable]; duplicate {
			return nil, fmt.Errorf("package execution catalog: duplicate wrapper %q", w.Executable)
		}
		out.wrappers[w.Executable] = w
	}
	return out, nil
}

func validWrapperMode(mode string) bool {
	switch mode {
	case "command_after_options", "command_after_options_and_assignments", "module_command", "shell_command":
		return true
	default:
		return false
	}
}

func validPackageMode(mode string) bool {
	switch mode {
	case "first_positional", "flag_only", "flag_or_first_positional", "all_positionals", "versioned_positionals", "module_paths", "maven_plugin_coordinates":
		return true
	default:
		return false
	}
}

func (c *catalog) classify(stages []exec.Stage) (*Execution, bool) {
	if c == nil {
		return nil, false
	}
	matches := c.classifyStages(stages, 0)
	if len(matches) == 0 {
		return nil, false
	}
	return executionFromMatches(matches)
}

func (c *catalog) classifyStages(stages []exec.Stage, depth int) []classified {
	if depth > 4 {
		return nil
	}
	var matches []classified
	for _, stage := range stages {
		executable := filepath.Base(strings.TrimSpace(stage.Name))
		matched := false
		for _, candidate := range c.byExecutable[executable] {
			if found, ok := classifyRule(candidate, stage.Args); ok {
				matches = append(matches, found)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if inner, ok := unwrapStage(c.wrappers[executable], stage.Args); ok {
			matches = append(matches, c.classifyStages(inner, depth+1)...)
		}
	}
	return matches
}

func executionFromMatches(matches []classified) (*Execution, bool) {
	execution := &Execution{}
	managerIDs := map[string]struct{}{}
	operations := map[Operation]struct{}{}
	for _, match := range matches {
		managerIDs[match.manager.ID] = struct{}{}
		operations[match.rule.Operation] = struct{}{}
		execution.AllowedHosts = append(execution.AllowedHosts, match.manager.registryHosts...)
		execution.AllowedHosts = append(execution.AllowedHosts, match.manager.DownloadHosts...)
		for _, spec := range match.packages {
			name, version := splitPackageSpec(match.manager.System, spec, match.version)
			if name == "" {
				continue
			}
			execution.Packages = append(execution.Packages, Package{
				System: match.manager.System, Name: name, RequestedVersion: version,
			})
		}
		execution.AllowedHosts = append(execution.AllowedHosts, match.registryHosts...)
	}
	if len(execution.Packages) == 0 {
		return nil, false
	}
	execution.Manager = strings.Join(sortedKeys(managerIDs), "+")
	if len(operations) == 1 {
		for op := range operations {
			execution.Operation = op
		}
	} else {
		execution.Operation = OperationRemoteExecute
	}
	execution.AllowedHosts = normalizedStrings(execution.AllowedHosts)
	return execution, true
}

func unwrapStage(w wrapper, args []string) ([]exec.Stage, bool) {
	if w.Executable == "" {
		return nil, false
	}
	switch w.Mode {
	case "module_command":
		flags := stringSet(w.CommandFlags)
		for i, arg := range args {
			if _, ok := flags[arg]; ok && i+1 < len(args) {
				return []exec.Stage{{Name: args[i+1], Args: append([]string(nil), args[i+2:]...)}}, true
			}
		}
		return nil, false
	case "shell_command":
		flags := stringSet(w.CommandFlags)
		for i, arg := range args {
			if _, ok := flags[arg]; ok && i+1 < len(args) {
				stages, err := exec.StagesFromCommandLine(args[i+1])
				return stages, err == nil
			}
		}
		return nil, false
	case "command_after_options", "command_after_options_and_assignments":
		valueFlags := stringSet(w.ValueFlags)
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--" {
				i++
				if i < len(args) {
					return []exec.Stage{{Name: args[i], Args: append([]string(nil), args[i+1:]...)}}, true
				}
				return nil, false
			}
			key, _, inline := strings.Cut(arg, "=")
			if _, ok := valueFlags[key]; ok {
				if !inline {
					i++
				}
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if w.Mode == "command_after_options_and_assignments" && strings.Contains(arg, "=") {
				continue
			}
			return []exec.Stage{{Name: arg, Args: append([]string(nil), args[i+1:]...)}}, true
		}
	}
	return nil, false
}

type classified struct {
	manager       manager
	rule          commandRule
	packages      []string
	version       string
	registryHosts []string
}

func classifyRule(candidate compiledRule, args []string) (classified, bool) {
	r := candidate.rule
	if len(args) < len(r.Verbs) {
		return classified{}, false
	}
	for i, verb := range r.Verbs {
		if args[i] != verb {
			return classified{}, false
		}
	}
	rest := args[len(r.Verbs):]
	valueFlags := stringSet(r.ValueFlags)
	packageFlags := stringSet(r.PackageFlags)
	registryFlags := stringSet(r.RegistryFlags)
	versionFlags := stringSet(r.VersionFlags)
	var packages []string
	var registryHosts []string
	version := ""
	positional := make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if arg == "--" {
			positional = append(positional, rest[i+1:]...)
			break
		}
		key, inline, hasInline := strings.Cut(arg, "=")
		if _, ok := packageFlags[key]; ok {
			if hasInline {
				packages = append(packages, inline)
			} else if i+1 < len(rest) {
				i++
				packages = append(packages, rest[i])
			}
			continue
		}
		if _, ok := registryFlags[key]; ok {
			value := inline
			if !hasInline && i+1 < len(rest) {
				i++
				value = rest[i]
			}
			if host := registryHost(value); host != "" {
				registryHosts = append(registryHosts, host)
			}
			continue
		}
		if _, ok := versionFlags[key]; ok {
			if hasInline {
				version = inline
			} else if i+1 < len(rest) {
				i++
				version = rest[i]
			}
			continue
		}
		if _, ok := valueFlags[key]; ok {
			if !hasInline && i+1 < len(rest) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") || isLocalPath(arg) {
			continue
		}
		positional = append(positional, arg)
	}
	switch r.PackageMode {
	case "first_positional":
		if len(positional) > 0 {
			packages = append(packages, positional[0])
		}
	case "flag_or_first_positional":
		if len(packages) == 0 && len(positional) > 0 {
			packages = append(packages, positional[0])
		}
	case "all_positionals":
		packages = append(packages, positional...)
	case "versioned_positionals":
		for _, item := range positional {
			if strings.LastIndex(item, "@") > strings.LastIndex(item, "/") {
				packages = append(packages, item)
			}
		}
	case "module_paths":
		for _, item := range positional {
			if isModulePath(item) {
				packages = append(packages, item)
			}
		}
	case "flag_only":
	case "maven_plugin_coordinates":
		for _, item := range positional {
			if strings.Count(item, ":") >= 2 {
				packages = append(packages, item)
				break
			}
		}
	}
	packages = normalizedStrings(packages)
	return classified{
		manager: candidate.manager, rule: r, packages: packages, version: version,
		registryHosts: normalizedStrings(registryHosts),
	}, len(packages) > 0
}

// isLocalPath reports a positional naming local source rather than a registry package.
func isLocalPath(arg string) bool {
	switch {
	case arg == ".", arg == "..":
		return true
	case strings.HasPrefix(arg, "./"), strings.HasPrefix(arg, "../"), strings.HasPrefix(arg, "/"), strings.HasPrefix(arg, "~/"), strings.HasPrefix(arg, "file:"):
		return true
	}
	return false
}

// isModulePath accepts a remote module path, whose first element is a host name,
// and rejects local package patterns such as ./... or all.
func isModulePath(item string) bool {
	if strings.HasPrefix(item, ".") || strings.HasPrefix(item, "/") {
		return false
	}
	first, _, _ := strings.Cut(item, "/")
	first, _, _ = strings.Cut(first, "@")
	return strings.Contains(first, ".")
}

func registryHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		parsed, err = url.Parse("https://" + raw)
	}
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func splitPackageSpec(system, spec, versionFlag string) (string, string) {
	spec = strings.TrimSpace(spec)
	versionFlag = strings.TrimSpace(versionFlag)
	if spec == "" {
		return "", ""
	}
	if system == "MAVEN" {
		parts := strings.Split(spec, ":")
		if len(parts) < 3 {
			return "", ""
		}
		return parts[0] + ":" + parts[1], parts[2]
	}
	if versionFlag != "" {
		return spec, versionFlag
	}
	if system == "PYPI" {
		if name, version, ok := strings.Cut(spec, "=="); ok {
			return strings.TrimSpace(name), strings.TrimSpace(version)
		}
	}
	if system == "NUGET" || system == "RUBYGEMS" {
		return spec, ""
	}
	// A leading @ belongs to the name; only a later @ separates a version.
	if at := strings.LastIndex(spec, "@"); at > strings.LastIndex(spec, "/") {
		return strings.TrimSpace(spec[:at]), strings.TrimSpace(spec[at+1:])
	}
	return spec, ""
}

func stringSet(items []string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

func trimmedStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func normalizedStrings(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, duplicate := seen[item]; duplicate {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[T ~string](items map[T]struct{}) []string {
	out := make([]string, 0, len(items))
	for item := range items {
		out = append(out, string(item))
	}
	sort.Strings(out)
	return out
}
