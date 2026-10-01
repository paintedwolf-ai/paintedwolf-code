package detectionpack

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/lycaon/lycaon/internal/egress"
)

// ResolveEffectReach classifies declared action destinations.
func ResolveEffectReach(tool string, args map[string]any, projectDir string, roots []string) string {
	cmd := CommandTextForTool(tool, args)
	return resolveCommandEffectReach(tool, cmd, args, projectDir, roots)
}

// resolveCommandEffectReach classifies one process command.
func resolveCommandEffectReach(tool, cmd string, args map[string]any, projectDir string, roots []string) string {
	if reach, ok := resolveExplicitEndpointReach(cmd, args, roots); ok {
		return reach
	}
	if strings.TrimSpace(cmd) == "" {
		return EffectReachUnproven
	}
	images := newCommandEvent(tool, cmd, projectDir, true, "proxy", "", nil).Image
	// Image-specific resolvers read declared configuration.
	for _, image := range images {
		switch image {
		case "cargo":
			if reach, ok := resolveCargoPublishReach(cmd, args, projectDir, roots); ok {
				return reach
			}
		case "npm", "pnpm", "yarn", "bun":
			if reach, ok := resolveNpmPublishReach(cmd, args, projectDir, roots); ok {
				return reach
			}
			if reach, ok := resolveNpmRegistryRedirectReach(cmd, roots); ok {
				return reach
			}
		case "twine", "poetry", "flit", "hatch":
			if reach, ok := resolvePypiPublishReach(cmd, args, projectDir, roots); ok {
				return reach
			}
		case "uv", "pip", "pip3":
			if reach, ok := resolvePypiPublishReach(cmd, args, projectDir, roots); ok {
				return reach
			}
			if reach, ok := resolvePythonIndexReach(cmd, roots); ok {
				return reach
			}
		case "docker", "podman", "nerdctl":
			if reach, ok := resolveContainerPushReach(cmd); ok {
				return reach
			}
		case "go":
			if reach, ok := resolveGoProxyReach(cmd, args, roots); ok {
				return reach
			}
		case "git":
			if reach, ok := resolveGitWorktreeReach(images, cmd, args, projectDir, roots); ok {
				return reach
			}
		}
	}
	if reach, ok := resolveFilesystemReach(images, cmd, args, projectDir, roots); ok {
		return reach
	}
	return EffectReachUnproven
}

// resolveExplicitEndpointReach reads declared endpoint options and environment.
func resolveExplicitEndpointReach(cmd string, args map[string]any, roots []string) (string, bool) {
	for _, flag := range []string{"--endpoint-url", "--endpoint", "--base-url"} {
		if v := flagValue(cmd, flag); v != "" {
			return classifyDestination(v, roots), true
		}
	}
	for _, path := range []string{
		"endpoint_url", "endpoint", "base_url",
		"config.endpoint_url", "config.endpoint", "config.base_url",
		"client.endpoint_url", "client.endpoint", "client.base_url",
	} {
		if value, ok := lookupArgPath(args, path); ok {
			if raw, ok := value.(string); ok && strings.TrimSpace(raw) != "" {
				return classifyDestination(raw, roots), true
			}
		}
	}
	for _, key := range []string{
		"AWS_ENDPOINT_URL",
		"AWS_ENDPOINT_URL_S3",
		"AWS_ENDPOINT_URL_IAM",
		"LOCALSTACK_HOSTNAME",
		"LOCALSTACK_HOST",
	} {
		if v := argEnv(args, key); v != "" {
			if !strings.Contains(v, "://") {
				v = "http://" + v
			}
			return classifyDestination(v, roots), true
		}
	}
	return "", false
}

func resolveNpmRegistryRedirectReach(cmd string, roots []string) (string, bool) {
	if reg := flagValue(cmd, "--registry"); reg != "" {
		return classifyDestination(reg, roots), true
	}
	// Registry configuration may use positional syntax.
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "registry" && i > 0 && (fields[i-1] == "set" || fields[i-1] == "config") {
			if i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") {
				return classifyDestination(fields[i+1], roots), true
			}
		}
	}
	if strings.Contains(NormalizeCommandLine(cmd), " config set registry ") {
		if v := flagValueAfter(cmd, "registry"); v != "" {
			return classifyDestination(v, roots), true
		}
	}
	return "", false
}

func resolvePythonIndexReach(cmd string, roots []string) (string, bool) {
	for _, flag := range []string{"--index-url", "--extra-index-url", "--find-links"} {
		if v := flagValue(cmd, flag); v != "" {
			return classifyDestination(v, roots), true
		}
	}
	return "", false
}

func resolveGoProxyReach(cmd string, args map[string]any, roots []string) (string, bool) {
	if v := flagValue(cmd, "-w"); v != "" && strings.HasPrefix(strings.ToUpper(v), "GOPROXY=") {
		return classifyDestination(strings.TrimPrefix(v, "GOPROXY="), roots), true
	}
	// The proxy assignment may precede the command.
	fields := strings.Fields(cmd)
	for _, f := range fields {
		if strings.HasPrefix(f, "GOPROXY=") {
			return classifyDestination(strings.TrimPrefix(f, "GOPROXY="), roots), true
		}
	}
	if v := argEnv(args, "GOPROXY"); v != "" {
		return classifyDestination(v, roots), true
	}
	return "", false
}

func flagValueAfter(cmd, key string) string {
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == key {
			return strings.Trim(fields[i+1], `"'`)
		}
	}
	return ""
}

func resolveCargoPublishReach(cmd string, args map[string]any, projectDir string, roots []string) (string, bool) {
	// publish and yank share the same registry destination model.
	if !commandHasToken(cmd, "publish") && !commandHasToken(cmd, "yank") {
		return "", false
	}
	name := flagValue(cmd, "--registry")
	if name == "" {
		return EffectReachRemote, true
	}
	index := lookupCargoRegistryIndex(name, args, projectDir)
	if index == "" {
		return EffectReachUnproven, true
	}
	return classifyDestination(index, roots), true
}

func resolveNpmPublishReach(cmd string, args map[string]any, projectDir string, roots []string) (string, bool) {
	// publish and unpublish share the same registry destination model.
	if !commandHasToken(cmd, "publish") && !commandHasToken(cmd, "unpublish") {
		return "", false
	}
	if reg := flagValue(cmd, "--registry"); reg != "" {
		return classifyDestination(reg, roots), true
	}
	if reg := readNpmrcRegistry(args, projectDir); reg != "" {
		return classifyDestination(reg, roots), true
	}
	return EffectReachRemote, true
}

func resolvePypiPublishReach(cmd string, args map[string]any, projectDir string, roots []string) (string, bool) {
	normalized := NormalizeCommandLine(cmd)
	isUpload := strings.Contains(normalized, " upload ") || commandHasToken(cmd, "publish")
	if !isUpload {
		return "", false
	}
	for _, flag := range []string{"--repository-url", "--index-url"} {
		if v := flagValue(cmd, flag); v != "" {
			return classifyDestination(v, roots), true
		}
	}
	_ = args
	_ = projectDir
	return EffectReachRemote, true
}

func resolveContainerPushReach(cmd string) (string, bool) {
	normalized := NormalizeCommandLine(cmd)
	if !strings.Contains(normalized, " push ") && !strings.Contains(normalized, " manifest push") {
		return "", false
	}
	ref := containerPushRef(cmd)
	if ref == "" {
		return EffectReachUnproven, true
	}
	host, ok := containerRegistryHost(ref)
	if !ok {
		return EffectReachUnproven, true
	}
	if host == "" {
		return EffectReachRemote, true
	}
	return classifyDestination("https://"+host, nil), true
}

func classifyDestination(raw string, roots []string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return EffectReachUnproven
	}
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		u, err := url.Parse(raw)
		if err != nil {
			return EffectReachUnproven
		}
		path := u.Path
		if path == "" {
			path = u.Opaque
		}
		if pathUnderRoots(path, roots) {
			return EffectReachLocal
		}
		return EffectReachUnproven
	}
	if !strings.Contains(raw, "://") {
		if pathUnderRoots(raw, roots) {
			return EffectReachLocal
		}
		if egress.SyntacticLoopback(raw) || egress.SyntacticLoopback(stripPort(raw)) {
			return EffectReachLocal
		}
		if looksLikeHostPort(raw) {
			if egress.SyntacticLoopback(stripPort(raw)) {
				return EffectReachLocal
			}
			return EffectReachRemote
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return EffectReachUnproven
	}
	host := u.Hostname()
	if host == "" {
		host = stripPort(u.Host)
	}
	if egress.SyntacticLoopback(host) {
		return EffectReachLocal
	}
	return EffectReachRemote
}

func stripPort(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err == nil {
		return host
	}
	return hostport
}

func looksLikeHostPort(v string) bool {
	if strings.Contains(v, "/") {
		return false
	}
	host, port, err := net.SplitHostPort(v)
	return err == nil && host != "" && port != ""
}

func pathUnderRoots(path string, roots []string) bool {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return false
	}
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" {
			continue
		}
		if path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func commandHasToken(cmd, token string) bool {
	return strings.Contains(NormalizeCommandLine(cmd), " "+token+" ")
}

func flagValue(cmd, flag string) string {
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == flag {
			if i+1 < len(fields) {
				return strings.Trim(fields[i+1], `"'`)
			}
			return ""
		}
		if strings.HasPrefix(f, flag+"=") {
			return strings.Trim(strings.TrimPrefix(f, flag+"="), `"'`)
		}
	}
	return ""
}

func lookupCargoRegistryIndex(name string, args map[string]any, projectDir string) string {
	for _, dir := range cargoConfigSearchDirs(args, projectDir) {
		path := filepath.Join(dir, ".cargo", "config.toml")
		if index := readCargoRegistryIndex(path, name); index != "" {
			return index
		}
		// Config files may omit the extension.
		path = filepath.Join(dir, ".cargo", "config")
		if index := readCargoRegistryIndex(path, name); index != "" {
			return index
		}
	}
	if home := argEnv(args, "CARGO_HOME"); home != "" {
		if index := readCargoRegistryIndex(filepath.Join(home, "config.toml"), name); index != "" {
			return index
		}
	}
	return ""
}

func cargoConfigSearchDirs(args map[string]any, projectDir string) []string {
	var dirs []string
	if cwd, _ := args["cwd"].(string); strings.TrimSpace(cwd) != "" {
		cwd = strings.TrimSpace(cwd)
		if !filepath.IsAbs(cwd) && projectDir != "" {
			cwd = filepath.Join(projectDir, cwd)
		}
		if filepath.IsAbs(cwd) {
			dirs = append(dirs, ancestorDirsThrough(cwd, projectDir)...)
		}
	}
	if projectDir != "" && !containsPath(dirs, projectDir) {
		dirs = append(dirs, projectDir)
	}
	if home := argEnv(args, "CARGO_HOME"); home != "" {
		dirs = append(dirs, filepath.Dir(home))
	}
	return dirs
}

// Config discovery includes ancestor directories within the attached project.
func ancestorDirsThrough(start, projectDir string) []string {
	start = filepath.Clean(start)
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "." || !pathUnderRoots(start, []string{projectDir}) {
		return []string{start}
	}
	var out []string
	for dir := start; ; dir = filepath.Dir(dir) {
		out = append(out, dir)
		if dir == projectDir {
			return out
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return out
		}
	}
}

func containsPath(paths []string, candidate string) bool {
	candidate = filepath.Clean(candidate)
	for _, path := range paths {
		if filepath.Clean(path) == candidate {
			return true
		}
	}
	return false
}

func readCargoRegistryIndex(path, name string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Registries map[string]struct {
			Index string `toml:"index"`
		} `toml:"registries"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return ""
	}
	if reg, ok := doc.Registries[name]; ok {
		return strings.TrimSpace(reg.Index)
	}
	return ""
}

func readNpmrcRegistry(args map[string]any, projectDir string) string {
	var dirs []string
	if cwd, _ := args["cwd"].(string); strings.TrimSpace(cwd) != "" {
		cwd = strings.TrimSpace(cwd)
		if filepath.IsAbs(cwd) {
			dirs = append(dirs, cwd)
		} else if projectDir != "" {
			dirs = append(dirs, filepath.Join(projectDir, cwd))
		}
	}
	if projectDir != "" {
		dirs = append(dirs, projectDir)
	}
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if strings.HasPrefix(line, "registry=") {
				return strings.TrimSpace(strings.TrimPrefix(line, "registry="))
			}
		}
	}
	return ""
}

func argEnv(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	env, _ := args["env"].(map[string]any)
	if env == nil {
		// JSON often decodes object values as map[string]string via other paths;
		// also accept map[string]string if present through typed copies.
		if envStr, ok := args["env"].(map[string]string); ok {
			return strings.TrimSpace(envStr[key])
		}
		return ""
	}
	if v, ok := env[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func containerPushRef(cmd string) string {
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields); i++ {
		if fields[i] != "push" {
			continue
		}
		// "manifest push <ref>"
		if i > 0 && fields[i-1] == "manifest" {
			if i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") {
				return fields[i+1]
			}
			continue
		}
		for j := i + 1; j < len(fields); j++ {
			if strings.HasPrefix(fields[j], "-") {
				// skip flag and its value when form is --flag value
				if !strings.Contains(fields[j], "=") && j+1 < len(fields) && !strings.HasPrefix(fields[j+1], "-") {
					j++
				}
				continue
			}
			return fields[j]
		}
	}
	return ""
}

// An empty host with ok=true denotes the default container registry.
func containerRegistryHost(ref string) (host string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	// Drop digest / tag for host detection; keep first path segment logic.
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return "", true // default registry
	}
	first := ref[:slash]
	if !strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost" {
		return "", true // default registry namespace
	}
	return first, true
}
