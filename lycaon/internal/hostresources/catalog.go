package hostresources

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Catalog struct {
	Version   int                   `yaml:"version"`
	Resources []Definition          `yaml:"resources"`
	Guidance  map[string]PromptMode `yaml:"guidance,omitempty"`
}

type Definition struct {
	ID           string             `yaml:"id"`
	Family       string             `yaml:"family"`
	Label        string             `yaml:"label"`
	Category     string             `yaml:"category"`
	Description  string             `yaml:"description"`
	DocsURL      string             `yaml:"docs_url"`
	Surfaces     []ExecutionSurface `yaml:"surfaces"`
	Realizations []Realization      `yaml:"realizations"`
	prompt       PromptMode
	origin       string
}

// Realization is one platform implementation of a stable host resource. A
// platform may match exactly one realization, which keeps discovery and
// authority details below the portable host-resource identity.
type Realization struct {
	Platforms   []string             `yaml:"platforms"`
	Discover    Expression           `yaml:"discover"`
	Connections []ConnectionTemplate `yaml:"connections"`
	// WriteRoots are catalogued daemon state directories. Templates:
	// {home}, {config_dir}, {runtime_dir}.
	WriteRoots []string `yaml:"write_roots,omitempty"`
}

type ConnectionTemplate struct {
	Mode       ConnectionMode        `yaml:"mode"`
	Transport  LocalServiceTransport `yaml:"transport"`
	Target     string                `yaml:"target"`
	TargetFrom string                `yaml:"target_from"`
}

// Expression is a closed declarative probe tree. Exactly one member is allowed.
type Expression struct {
	All          []Expression       `yaml:"all"`
	Any          []Expression       `yaml:"any"`
	Executable   *ExecutableProbe   `yaml:"executable"`
	Path         *PathProbe         `yaml:"path"`
	Environment  *EnvironmentProbe  `yaml:"environment"`
	LoopbackHTTP *LoopbackHTTPProbe `yaml:"loopback_http"`
	NamedPipe    *NamedPipeProbe    `yaml:"named_pipe"`
}

type ExecutableProbe struct {
	Names   []string `yaml:"names"`
	Capture string   `yaml:"capture"`
}

type PathProbe struct {
	Paths   []string `yaml:"paths"`
	Kind    string   `yaml:"kind"`
	Capture string   `yaml:"capture"`
}

type EnvironmentProbe struct {
	Names []string `yaml:"names"`
}

type LoopbackHTTPProbe struct {
	URLs      []string `yaml:"urls"`
	Status    int      `yaml:"status"`
	Capture   string   `yaml:"capture"`
	TimeoutMS int      `yaml:"timeout_ms"`
}

type NamedPipeProbe struct {
	Names   []string `yaml:"names"`
	Capture string   `yaml:"capture"`
}

var (
	idPattern          = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*$`)
	capturePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	binaryPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	environmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	windowsPathPattern = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

func ParseCatalog(data []byte, origin string) (Catalog, error) {
	if len(data) > CatalogBytesMax {
		return Catalog{}, fmt.Errorf("host resources catalog: size exceeds %d bytes", CatalogBytesMax)
	}
	var catalog Catalog
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("parse host resources catalog: %w", err)
	}
	if err := validateCatalog(&catalog, origin); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func validateCatalog(catalog *Catalog, origin string) error {
	if catalog.Version != CatalogVersion {
		return fmt.Errorf("host resources catalog: version %d is unsupported", catalog.Version)
	}
	if len(catalog.Resources) == 0 && origin == "built-in" {
		return fmt.Errorf("host resources catalog: no resources")
	}
	if len(catalog.Resources) > CatalogMax {
		return fmt.Errorf("host resources catalog: %d entries exceeds limit %d", len(catalog.Resources), CatalogMax)
	}
	if len(catalog.Guidance) > CatalogMax {
		return fmt.Errorf("host resources catalog: %d guidance entries exceeds limit %d", len(catalog.Guidance), CatalogMax)
	}
	if len(catalog.Resources) == 0 && len(catalog.Guidance) == 0 {
		return fmt.Errorf("host resources catalog: requires resources or guidance")
	}
	seen := make(map[string]struct{}, len(catalog.Resources))
	for i := range catalog.Resources {
		def := &catalog.Resources[i]
		def.ID = strings.TrimSpace(def.ID)
		def.Family = strings.TrimSpace(def.Family)
		def.Label = strings.TrimSpace(def.Label)
		def.Category = strings.TrimSpace(def.Category)
		def.Description = strings.TrimSpace(def.Description)
		def.DocsURL = strings.TrimSpace(def.DocsURL)
		def.origin = origin
		def.prompt = PromptOmit
		if origin == "user" {
			def.prompt = PromptAdvertise
		}
		if len(def.ID) > IdentifierBytesMax || !idPattern.MatchString(def.ID) {
			return fmt.Errorf("host resources catalog: invalid id %q", def.ID)
		}
		if _, duplicate := seen[def.ID]; duplicate {
			return fmt.Errorf("host resources catalog: duplicate id %q", def.ID)
		}
		seen[def.ID] = struct{}{}
		if len(def.Family) > IdentifierBytesMax || !idPattern.MatchString(def.Family) {
			return fmt.Errorf("host resources catalog: %q has invalid family %q", def.ID, def.Family)
		}
		if def.Label == "" || def.Category == "" || def.Description == "" {
			return fmt.Errorf("host resources catalog: %q requires family, label, category, and description", def.ID)
		}
		if len(def.Label) > 120 || len(def.Category) > 80 || len(def.Description) > 1024 {
			return fmt.Errorf("host resources catalog: %q text field exceeds its limit", def.ID)
		}
		if def.DocsURL != "" {
			u, err := url.Parse(def.DocsURL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(def.DocsURL) > 2048 {
				return fmt.Errorf("host resources catalog: %q docs_url must be HTTPS", def.ID)
			}
		}
		if err := validateSurfaces(def.ID, def.Surfaces); err != nil {
			return err
		}
		if err := validateRealizations(*def); err != nil {
			return err
		}
	}
	for rawID, rawMode := range catalog.Guidance {
		id := strings.TrimSpace(rawID)
		if len(id) > IdentifierBytesMax || !idPattern.MatchString(id) {
			return fmt.Errorf("host resources catalog: invalid guidance id %q", rawID)
		}
		mode := PromptMode(strings.TrimSpace(string(rawMode)))
		if !validPromptMode(mode) {
			return fmt.Errorf("host resources catalog: %q has invalid prompt guidance %q", id, rawMode)
		}
		if id != rawID || mode != rawMode {
			delete(catalog.Guidance, rawID)
			catalog.Guidance[id] = mode
		}
	}
	sort.Slice(catalog.Resources, func(i, j int) bool {
		return catalog.Resources[i].ID < catalog.Resources[j].ID
	})
	return nil
}

func validateSurfaces(id string, surfaces []ExecutionSurface) error {
	if len(surfaces) == 0 || len(surfaces) > ProbeValuesMax {
		return fmt.Errorf("host resources catalog: %q requires 1 to %d execution surfaces", id, ProbeValuesMax)
	}
	seen := map[ExecutionSurface]struct{}{}
	for _, surface := range surfaces {
		if surface != SurfaceProcessExec {
			return fmt.Errorf("host resources catalog: %q has invalid execution surface %q", id, surface)
		}
		if _, duplicate := seen[surface]; duplicate {
			return fmt.Errorf("host resources catalog: %q repeats execution surface %q", id, surface)
		}
		seen[surface] = struct{}{}
	}
	return nil
}

func validateRealizations(def Definition) error {
	if len(def.Realizations) == 0 || len(def.Realizations) > ProbeValuesMax {
		return fmt.Errorf("host resources catalog: %q requires 1 to %d platform realizations", def.ID, ProbeValuesMax)
	}
	claimed := map[string]struct{}{}
	for i, realization := range def.Realizations {
		if err := validatePlatforms(def.ID, realization.Platforms); err != nil {
			return err
		}
		platforms := realization.Platforms
		if len(platforms) == 0 {
			platforms = []string{"macos", "linux", "windows"}
		}
		for _, platform := range platforms {
			if _, overlap := claimed[platform]; overlap {
				return fmt.Errorf("host resources catalog: %q has overlapping %q realizations", def.ID, platform)
			}
			claimed[platform] = struct{}{}
		}
		nodes := 0
		if err := validateExpression(def.ID, realization.Discover, 1, &nodes); err != nil {
			return fmt.Errorf("realization %d: %w", i+1, err)
		}
		if err := validateConnections(def.ID, realization.Connections, realization.Discover); err != nil {
			return fmt.Errorf("realization %d: %w", i+1, err)
		}
		if err := validateWriteRoots(def.ID, realization.WriteRoots); err != nil {
			return fmt.Errorf("realization %d: %w", i+1, err)
		}
	}
	return nil
}

func validateWriteRoots(id string, roots []string) error {
	if len(roots) > ProbeValuesMax {
		return fmt.Errorf("host resources catalog: %q has too many write roots", id)
	}
	seen := map[string]struct{}{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if !validPathTemplate(root) {
			return fmt.Errorf("host resources catalog: %q has invalid write root %q", id, root)
		}
		if _, dup := seen[root]; dup {
			return fmt.Errorf("host resources catalog: %q repeats write root %q", id, root)
		}
		seen[root] = struct{}{}
	}
	return nil
}

func validPromptMode(mode PromptMode) bool {
	switch mode {
	case PromptOmit, PromptAdvertise, PromptAvoid:
		return true
	default:
		return false
	}
}

// Prompt is the catalog guidance, or omit when unset or invalid.
func (d Definition) Prompt() PromptMode {
	if validPromptMode(d.prompt) {
		return d.prompt
	}
	return PromptOmit
}

func validatePlatforms(id string, platforms []string) error {
	for _, platform := range platforms {
		switch platform {
		case "macos", "linux", "windows":
		default:
			return fmt.Errorf("host resources catalog: %q has invalid platform %q", id, platform)
		}
	}
	return nil
}

func validateExpression(id string, expr Expression, depth int, nodes *int) error {
	(*nodes)++
	if depth > ExpressionDepthMax || *nodes > ExpressionNodesMax {
		return fmt.Errorf("host resources catalog: %q discovery expression exceeds complexity limits", id)
	}
	variants := 0
	if len(expr.All) > 0 {
		variants++
	}
	if len(expr.Any) > 0 {
		variants++
	}
	if expr.Executable != nil {
		variants++
	}
	if expr.Path != nil {
		variants++
	}
	if expr.Environment != nil {
		variants++
	}
	if expr.LoopbackHTTP != nil {
		variants++
	}
	if expr.NamedPipe != nil {
		variants++
	}
	if variants != 1 {
		return fmt.Errorf("host resources catalog: %q discovery expression must contain exactly one probe", id)
	}
	children := append(append([]Expression(nil), expr.All...), expr.Any...)
	if len(children) > ProbeValuesMax {
		return fmt.Errorf("host resources catalog: %q discovery expression has too many children", id)
	}
	for _, child := range children {
		if err := validateExpression(id, child, depth+1, nodes); err != nil {
			return err
		}
	}
	if probe := expr.Executable; probe != nil {
		if len(probe.Names) == 0 || len(probe.Names) > ProbeValuesMax {
			return fmt.Errorf("host resources catalog: %q executable probe requires names", id)
		}
		for _, name := range probe.Names {
			if len(name) > ExecutableBytesMax || !binaryPattern.MatchString(name) {
				return fmt.Errorf("host resources catalog: %q has invalid executable %q", id, name)
			}
		}
		if err := validateCapture(id, probe.Capture); err != nil {
			return err
		}
	}
	if probe := expr.Path; probe != nil {
		if len(probe.Paths) == 0 || len(probe.Paths) > ProbeValuesMax {
			return fmt.Errorf("host resources catalog: %q path probe requires paths", id)
		}
		switch probe.Kind {
		case "file", "directory", "socket", "any":
		default:
			return fmt.Errorf("host resources catalog: %q has invalid path kind %q", id, probe.Kind)
		}
		for _, path := range probe.Paths {
			if !validPathTemplate(path) {
				return fmt.Errorf("host resources catalog: %q has invalid path", id)
			}
		}
		if err := validateCapture(id, probe.Capture); err != nil {
			return err
		}
	}
	if probe := expr.Environment; probe != nil {
		if len(probe.Names) == 0 || len(probe.Names) > ProbeValuesMax {
			return fmt.Errorf("host resources catalog: %q environment probe requires names", id)
		}
		for _, name := range probe.Names {
			if len(name) > EnvironmentBytesMax || !environmentPattern.MatchString(name) {
				return fmt.Errorf("host resources catalog: %q has invalid environment name", id)
			}
		}
	}
	if probe := expr.LoopbackHTTP; probe != nil {
		if len(probe.URLs) == 0 || len(probe.URLs) > ProbeValuesMax {
			return fmt.Errorf("host resources catalog: %q loopback_http probe requires urls", id)
		}
		if probe.Status == 0 {
			probe.Status = 200
		}
		if probe.Status < 100 || probe.Status > 599 {
			return fmt.Errorf("host resources catalog: %q has invalid HTTP status", id)
		}
		if probe.TimeoutMS == 0 {
			probe.TimeoutMS = 500
		}
		if probe.TimeoutMS < 50 || probe.TimeoutMS > 2000 {
			return fmt.Errorf("host resources catalog: %q HTTP timeout must be 50-2000ms", id)
		}
		for _, raw := range probe.URLs {
			if len(raw) > URLBytesMax {
				return fmt.Errorf("host resources catalog: %q loopback_http URL exceeds %d bytes", id, URLBytesMax)
			}
			if err := validateLoopbackURL(raw); err != nil {
				return fmt.Errorf("host resources catalog: %q: %w", id, err)
			}
		}
		if err := validateCapture(id, probe.Capture); err != nil {
			return err
		}
	}
	if probe := expr.NamedPipe; probe != nil {
		if len(probe.Names) == 0 || len(probe.Names) > ProbeValuesMax {
			return fmt.Errorf("host resources catalog: %q named_pipe probe requires names", id)
		}
		for _, name := range probe.Names {
			if !validNamedPipe(name) {
				return fmt.Errorf("host resources catalog: %q has invalid named pipe", id)
			}
		}
		if err := validateCapture(id, probe.Capture); err != nil {
			return err
		}
	}
	return nil
}

func validNamedPipe(name string) bool {
	name = strings.TrimSpace(name)
	return strings.HasPrefix(strings.ToLower(name), `\\.\pipe\`) &&
		len(name) <= 1024 && !strings.ContainsRune(name, 0)
}

func validPathTemplate(template string) bool {
	value := strings.TrimSpace(template)
	if value == "" || len(value) > PathBytesMax || strings.ContainsRune(value, 0) {
		return false
	}
	for _, token := range []string{"{home}", "{config_dir}", "{runtime_dir}"} {
		value = strings.ReplaceAll(value, token, "/root")
	}
	if strings.ContainsAny(value, "{}") {
		return false
	}
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) || windowsPathPattern.MatchString(value)
}

func validateCapture(id, capture string) error {
	if capture != "" && (len(capture) > IdentifierBytesMax || !capturePattern.MatchString(capture)) {
		return fmt.Errorf("host resources catalog: %q has invalid capture %q", id, capture)
	}
	return nil
}

func validateConnections(id string, connections []ConnectionTemplate, discover Expression) error {
	if len(connections) > ProbeValuesMax {
		return fmt.Errorf("host resources catalog: %q has too many connections", id)
	}
	captures := expressionCaptures(discover)
	for _, connection := range connections {
		switch connection.Mode {
		case ConnectionNone, ConnectionProxy, ConnectionSOCKS, ConnectionBaselineLoopback,
			ConnectionLocalService, ConnectionDirectIP, ConnectionDynamic:
		default:
			return fmt.Errorf("host resources catalog: %q has invalid connection mode %q", id, connection.Mode)
		}
		if connection.Target != "" && connection.TargetFrom != "" {
			return fmt.Errorf("host resources catalog: %q connection cannot set target and target_from", id)
		}
		if err := validateCapture(id, connection.TargetFrom); err != nil {
			return err
		}
		if len(connection.Target) > PathBytesMax || strings.ContainsRune(connection.Target, 0) {
			return fmt.Errorf("host resources catalog: %q has invalid connection target", id)
		}
		captureKind, found := captures[connection.TargetFrom]
		if connection.TargetFrom != "" && !found {
			return fmt.Errorf("host resources catalog: %q connection references unknown capture %q", id, connection.TargetFrom)
		}
		if connection.Mode == ConnectionLocalService && connection.TargetFrom == "" {
			return fmt.Errorf("host resources catalog: %q local_service connection requires target_from", id)
		}
		if connection.Mode == ConnectionLocalService && !validLocalServiceTransport(connection.Transport) {
			return fmt.Errorf("host resources catalog: %q local_service connection requires a valid transport", id)
		}
		if connection.Mode != ConnectionLocalService && connection.Transport != "" {
			return fmt.Errorf("host resources catalog: %q transport is valid only for local_service", id)
		}
		if connection.Mode == ConnectionLocalService &&
			(connection.Transport == LocalServiceUnixSocket && captureKind != "socket" ||
				connection.Transport == LocalServiceNamedPipe && captureKind != "named_pipe") {
			return fmt.Errorf("host resources catalog: %q local_service transport does not match its discovery capture", id)
		}
		if connection.Mode == ConnectionBaselineLoopback && connection.Target != "" {
			if err := validateLoopbackURL(connection.Target); err != nil {
				return fmt.Errorf("host resources catalog: %q connection target: %w", id, err)
			}
		}
	}
	return nil
}

func validLocalServiceTransport(transport LocalServiceTransport) bool {
	return transport == LocalServiceUnixSocket || transport == LocalServiceNamedPipe
}

func expressionCaptures(expr Expression) map[string]string {
	out := map[string]string{}
	for _, child := range append(append([]Expression(nil), expr.All...), expr.Any...) {
		for name, kind := range expressionCaptures(child) {
			out[name] = kind
		}
	}
	if probe := expr.Executable; probe != nil && probe.Capture != "" {
		out[probe.Capture] = "executable"
	}
	if probe := expr.Path; probe != nil && probe.Capture != "" {
		out[probe.Capture] = probe.Kind
	}
	if probe := expr.LoopbackHTTP; probe != nil && probe.Capture != "" {
		out[probe.Capture] = "loopback_http"
	}
	if probe := expr.NamedPipe; probe != nil && probe.Capture != "" {
		out[probe.Capture] = "named_pipe"
	}
	return out
}

func validateLoopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
		return fmt.Errorf("loopback_http URL must be an unauthenticated HTTP URL")
	}
	host := strings.ToLower(u.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("loopback_http host %q is not loopback", host)
	}
	if port := u.Port(); port != "" {
		value, portErr := strconv.Atoi(port)
		if portErr != nil || value < 1 || value > 65535 {
			return fmt.Errorf("loopback_http port is invalid")
		}
	}
	return nil
}

// RequirementGroup is one required slot in the Agent Skills metadata
// convention. The slot is satisfied when any single alternative resolves.
type RequirementGroup struct {
	Alternatives []string
}

// String renders the group in its authored form for diagnostics.
func (g RequirementGroup) String() string {
	return strings.Join(g.Alternatives, "|")
}

// ParseRequirementGroups parses the host-resources requirement grammar.
// Comma-separated entries are all required; a "|" between ids inside one
// entry lists interchangeable alternatives, any one of which satisfies that
// entry. Skill frontmatter accepts the full grammar; the exec-plane
// capability_request consumes it through ParseRequirements, which rejects
// multi-alternative entries.
func ParseRequirementGroups(raw string) ([]RequirementGroup, error) {
	seenGroups := map[string]struct{}{}
	var groups []RequirementGroup
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		seenIDs := map[string]struct{}{}
		var alternatives []string
		for _, alt := range strings.Split(part, "|") {
			id := strings.TrimSpace(alt)
			if id == "" {
				return nil, fmt.Errorf("host_resources entry %q has an empty alternative", strings.TrimSpace(part))
			}
			if len(id) > IdentifierBytesMax || !idPattern.MatchString(id) {
				return nil, fmt.Errorf("invalid host resource id %q", id)
			}
			if _, duplicate := seenIDs[id]; duplicate {
				continue
			}
			seenIDs[id] = struct{}{}
			alternatives = append(alternatives, id)
			if len(alternatives) > RequirementsMax {
				return nil, fmt.Errorf("host_resources entry exceeds %d alternatives", RequirementsMax)
			}
		}
		sort.Strings(alternatives)
		group := RequirementGroup{Alternatives: alternatives}
		if _, duplicate := seenGroups[group.String()]; duplicate {
			continue
		}
		seenGroups[group.String()] = struct{}{}
		groups = append(groups, group)
		if len(groups) > RequirementsMax {
			return nil, fmt.Errorf("host_resources metadata exceeds %d entries", RequirementsMax)
		}
	}
	if len(groups) == 0 && strings.TrimSpace(raw) != "" {
		return nil, fmt.Errorf("host_resources metadata contains no ids")
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].String() < groups[j].String() })
	return groups, nil
}

// ParseRequirements parses the flat exec-plane convention over the same
// grammar. Requirements are comma-separated host-resource ids, all required;
// the skill-metadata "|" alternative syntax is rejected here because a
// process start names the exact resources it uses.
func ParseRequirements(raw string) ([]string, error) {
	groups, err := ParseRequirementGroups(raw)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group.Alternatives) != 1 {
			return nil, fmt.Errorf("host resource alternatives %q are not valid here; name the exact id", group.String())
		}
		ids = append(ids, group.Alternatives[0])
	}
	return ids, nil
}
