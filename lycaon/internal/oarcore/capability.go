package oarcore

// The capability document, and the fact environment it produces.
//
// [OAR-FACT-21] An engine publishes a machine-readable capability document
// listing its supported anchors, the profiles it provides, its host-tier facts
// and their types, its activity window size, the detector references it
// resolves, whether it implements content mutation, and its condition parse-tree
// limit: everything a rule can be rejected for at load.
//
// [OAR-CONF-20] A conformance runner evaluates a fixture against the fixture's
// own capability document, so an Environment is built at run time from a
// document rather than compiled in.

import (
	"fmt"
	"regexp"
)

// CapabilityError is a capability document this engine refuses.
type CapabilityError struct{ Message string }

func (e *CapabilityError) Error() string { return e.Message }

func capErr(format string, args ...any) error {
	return &CapabilityError{Message: fmt.Sprintf(format, args...)}
}

// Tier is where a declared name lives: the core tier, a capability profile, or
// the engine's own host tier ([OAR-FACT-14]).
type Tier string

const (
	TierCore    Tier = "core"
	TierProfile Tier = "profile"
	TierHost    Tier = "host"
)

// FactDecl is one declared fact.
type FactDecl struct {
	Type    FactType
	Tier    Tier
	Profile string
}

// FunctionDecl is one declared observation function.
type FunctionDecl struct {
	Sig  FunctionSig
	Tier Tier
}

// Environment is the closed, typed fact environment a capability document
// declares ([OAR-FACT-1]). Every fact name and its type is fixed before any rule
// is compiled.
type Environment struct {
	// Anchors maps a core anchor identifier to the local anchor implementing it.
	Anchors map[string]string
	// HostAnchors is the declared host-native anchor catalogue ([OAR-PROF-5]),
	// written anchors.host. A rule whose anchor is neither a core anchor nor a
	// member of this catalogue is rejected at load, naming the value.
	HostAnchors        map[string]bool
	Profiles           map[string]bool
	Facts              map[string]FactDecl
	Functions          map[string]FunctionDecl
	ActivityWindow     int
	Detectors          map[string]bool
	ExpressionNodesMax int
	SupportsTransform  bool
}

var (
	namespaceRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	localAnchorRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	hostFactRE    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[A-Za-z_][A-Za-z0-9_]*$`)
	fnSigRE       = regexp.MustCompile(`^\((bool|int|double|string)\) -> (bool|int|double|string)$`)
	detectorRE    = regexp.MustCompile(`^detector://[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// [OAR-DOC-3] Each version part is a decimal integer with no leading zero:
	// "01.0" is not a version, because two engines free to disagree about
	// whether it equals "1.0" would disagree about which documents load.
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

// capabilityRequiredFields is everything [OAR-FACT-21] obliges a document to
// declare. `host_facts` is not here: a host with no host-tier fact declares
// none, and the empty list and the absent field say the same thing.
var capabilityRequiredFields = []string{
	"oar_capability_version",
	"host",
	"anchors",
	"profiles",
	"activity_window",
	"detectors",
	"expression_nodes_max",
	"supports_transform",
}

var capabilityKnownFields = append(append([]string(nil), capabilityRequiredFields...), "host_facts")

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// LoadCapability parses and validates a capability document into a declared fact
// environment.
func LoadCapability(doc any) (*Environment, error) {
	obj, ok := asObject(doc)
	if !ok {
		return nil, capErr("capability document is not an object")
	}

	for key := range obj {
		if !contains(capabilityKnownFields, key) {
			return nil, capErr("capability document carries undefined field %q", key)
		}
	}
	// [OAR-FACT-24] An engine validates its own document and refuses to start
	// when it is invalid, so an omission is a refusal here rather than a silent
	// default.
	for _, required := range capabilityRequiredFields {
		if _, present := obj[required]; !present {
			return nil, capErr("[OAR-FACT-21] capability document is missing required field %q", required)
		}
	}

	version, ok := obj["oar_capability_version"].(string)
	if !ok || version != "1.0" {
		return nil, capErr("capability document field \"oar_capability_version\" must be <major>.<minor>")
	}

	host, ok := obj["host"].(string)
	if !ok || !namespaceRE.MatchString(host) {
		return nil, capErr("capability document field \"host\" is not a namespace")
	}

	anchors, hostAnchors, err := loadAnchorMap(obj["anchors"])
	if err != nil {
		return nil, err
	}

	claimed, err := loadProfiles(obj["profiles"])
	if err != nil {
		return nil, err
	}

	// [OAR-FACT-14] Every declared fact belongs to exactly one profile, the core
	// tier, or the engine's own host tier. [OAR-FACT-16] a claimed profile is
	// provided whole. [OAR-FACT-17] nothing redefines a core or profile name.
	facts := make(map[string]FactDecl, 32)
	functions := make(map[string]FunctionDecl, 16)
	for name, typ := range coreFacts {
		facts[name] = FactDecl{Type: typ, Tier: TierCore}
	}
	// [OAR-FIRE-11] fire_count_of and breaker_count_of are core-tier observation
	// functions, so a rule reaches them without naming anything in requires.
	for name, sig := range coreFunctions {
		functions[name] = FunctionDecl{Sig: sig, Tier: TierCore}
	}
	for name := range claimed {
		def := profiles[name]
		for factName, typ := range def.Facts {
			facts[factName] = FactDecl{Type: typ, Tier: TierProfile, Profile: name}
		}
		for fnName, sig := range def.Functions {
			functions[fnName] = FunctionDecl{Sig: sig, Tier: TierProfile}
		}
	}

	if err := loadHostFacts(obj["host_facts"], host, facts, functions); err != nil {
		return nil, err
	}

	window, err := nonNegativeInt(obj["activity_window"])
	if err != nil {
		return nil, capErr("capability document field \"activity_window\" must be a non-negative integer")
	}

	detectors, err := loadDetectors(obj["detectors"])
	if err != nil {
		return nil, err
	}

	// [OAR-OPS-22], [OAR-FACT-24] Registered detectors produce the facts of the
	// content-safety and moderation profiles, so claiming one with no detectors
	// is invalid.
	for _, profile := range []string{"secrets", "pii", "prompt-injection", "jailbreak", "moderation"} {
		if claimed[profile] && len(detectors) == 0 {
			return nil, capErr("[OAR-OPS-22] capability document claims profile %q with an empty \"detectors\" list: its facts are produced by registered detectors", profile)
		}
	}

	// [OAR-EXPR-17] The declared parse-tree ceiling is at least 256 nodes.
	nodesMax, err := nonNegativeInt(obj["expression_nodes_max"])
	if err != nil || nodesMax < 256 {
		return nil, capErr("[OAR-EXPR-17] capability document field \"expression_nodes_max\" must be an integer of at least 256")
	}

	supportsTransform, ok := obj["supports_transform"].(bool)
	if !ok {
		return nil, capErr("capability document field \"supports_transform\" is not a boolean")
	}

	return &Environment{
		Anchors:            anchors,
		HostAnchors:        hostAnchors,
		Profiles:           claimed,
		Facts:              facts,
		Functions:          functions,
		ActivityWindow:     window,
		Detectors:          detectors,
		ExpressionNodesMax: nodesMax,
		SupportsTransform:  supportsTransform,
	}, nil
}

// loadAnchorMap reads the anchors section. [OAR-PROF-2] every core anchor
// appears exactly once across core and unsupported; a map omitting one or
// declaring one twice is rejected. [OAR-PROF-5] anchors.host is the declared
// host-native anchor catalogue.
func loadAnchorMap(raw any) (map[string]string, map[string]bool, error) {
	obj, ok := asObject(raw)
	if !ok {
		return nil, nil, capErr("capability document field \"anchors\" is missing")
	}
	for key := range obj {
		if key != "core" && key != "unsupported" && key != "host" {
			return nil, nil, capErr("[OAR-FACT-24] undefined anchors.%s", key)
		}
	}
	coreDoc, ok := asObject(obj["core"])
	if !ok {
		return nil, nil, capErr("capability document field \"anchors.core\" is missing")
	}
	anchors := make(map[string]string, len(coreDoc))
	for core, localRaw := range coreDoc {
		if !IsCoreAnchor(core) {
			return nil, nil, capErr("anchor profile map names %q, which is not a core anchor", core)
		}
		local, ok := localRaw.(string)
		if !ok || !localAnchorRE.MatchString(local) {
			return nil, nil, capErr("anchor profile map binds %s to a malformed local anchor", core)
		}
		anchors[core] = local
	}

	var unsupported []string
	if rawList, present := obj["unsupported"]; present {
		list, ok := rawList.([]any)
		if !ok {
			return nil, nil, capErr("capability document field \"anchors.unsupported\" is not a list")
		}
		for _, entry := range list {
			name, ok := entry.(string)
			if !ok || !IsCoreAnchor(name) {
				return nil, nil, capErr("anchor profile map lists %v as unsupported, which is not a core anchor", entry)
			}
			if contains(unsupported, name) {
				return nil, nil, capErr("[OAR-PROF-2] duplicate anchors.unsupported %s", name)
			}
			unsupported = append(unsupported, name)
		}
	}

	// [OAR-PROF-5] anchors.host catalogues the host-native anchors this host
	// accepts; absent and empty both mean none. A member spelled like a core
	// anchor is rejected.
	hostAnchors := map[string]bool{}
	if rawList, present := obj["host"]; present {
		list, ok := rawList.([]any)
		if !ok {
			return nil, nil, capErr("capability document field \"anchors.host\" is not a list")
		}
		for _, entry := range list {
			name, ok := entry.(string)
			if !ok || !localAnchorRE.MatchString(name) {
				return nil, nil, capErr("[OAR-PROF-5] host anchor catalogue lists malformed anchor %v", entry)
			}
			if IsCoreAnchor(name) {
				return nil, nil, capErr("[OAR-PROF-5] host anchor catalogue lists %s, which collides with a core anchor identifier", name)
			}
			if hostAnchors[name] {
				return nil, nil, capErr("[OAR-PROF-5] duplicate anchors.host %s", name)
			}
			hostAnchors[name] = true
		}
	}

	for _, core := range coreAnchors {
		_, inCore := anchors[core]
		inUnsupported := contains(unsupported, core)
		if inCore && inUnsupported {
			return nil, nil, capErr("[OAR-PROF-2] anchor profile map declares core anchor %s twice", core)
		}
		if !inCore && !inUnsupported {
			return nil, nil, capErr("[OAR-PROF-2] anchor profile map omits core anchor %s", core)
		}
	}
	return anchors, hostAnchors, nil
}

func loadProfiles(raw any) (map[string]bool, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, capErr("capability document field \"profiles\" is missing")
	}
	claimed := make(map[string]bool, len(list))
	for _, entry := range list {
		name, ok := entry.(string)
		if !ok {
			return nil, capErr("capability document field \"profiles\" holds a non-string")
		}
		if _, known := profiles[name]; !known {
			return nil, capErr("[OAR-FACT-24] capability document claims profile %q, which this specification does not define", name)
		}
		if claimed[name] {
			return nil, capErr("[OAR-FACT-24] duplicate profiles %s", name)
		}
		claimed[name] = true
	}
	return claimed, nil
}

// loadHostFacts declares the host tier. [OAR-FACT-18] each host fact is
// published under the host's registered namespace, so two engines cannot
// collide on a bare name.
func loadHostFacts(raw any, host string, facts map[string]FactDecl, functions map[string]FunctionDecl) error {
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return capErr("capability document field \"host_facts\" is not a list")
	}
	for _, entry := range list {
		obj, ok := asObject(entry)
		if !ok {
			return capErr("capability document field \"host_facts\" holds a non-object")
		}
		for key := range obj {
			if key != "name" && key != "type" && key != "observation" {
				return capErr("[OAR-FACT-27] undefined host_facts.%s", key)
			}
		}
		if observation, present := obj["observation"]; present {
			if _, ok := observation.(string); !ok {
				return capErr("[OAR-FACT-27] observation must be a string")
			}
		}
		name, ok := obj["name"].(string)
		if !ok || !hostFactRE.MatchString(name) {
			return capErr("[OAR-FACT-18] host fact %v is not published under a namespace registered to the host", obj["name"])
		}
		if len(name) <= len(host) || name[:len(host)+1] != host+"." {
			return capErr("[OAR-FACT-18] host fact %s is not published under the host namespace %s", name, host)
		}
		if _, taken := facts[name]; taken {
			return capErr("[OAR-FACT-17] host fact %s redefines a declared name", name)
		}
		if _, taken := functions[name]; taken {
			return capErr("[OAR-FACT-17] host fact %s redefines a declared name", name)
		}
		// [OAR-EXPR-24] An engine MUST NOT declare a fact or an observation
		// function under a reserved word. A name is one identifier however it is
		// spelled, so the check is against the whole name.
		if isReservedWord(name) {
			return capErr("[OAR-EXPR-24] host fact %s is declared under a reserved word", name)
		}
		typ, ok := obj["type"].(string)
		if !ok {
			return capErr("host fact %s has no type", name)
		}
		if m := fnSigRE.FindStringSubmatch(typ); m != nil {
			functions[name] = FunctionDecl{
				Sig:  FunctionSig{Arg: FactType(m[1]), Ret: FactType(m[2])},
				Tier: TierHost,
			}
			continue
		}
		if !isFactType(typ) {
			return capErr("[OAR-FACT-22] host fact %s declares type %q, which is not a fact type", name, typ)
		}
		facts[name] = FactDecl{Type: FactType(typ), Tier: TierHost}
	}
	return nil
}

func loadDetectors(raw any) (map[string]bool, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, capErr("capability document field \"detectors\" is not a list")
	}
	out := make(map[string]bool, len(list))
	for _, entry := range list {
		ref, ok := entry.(string)
		if !ok || !detectorRE.MatchString(ref) {
			return nil, capErr("capability document lists a malformed detector reference %v", entry)
		}
		if out[ref] {
			return nil, capErr("[OAR-FACT-24] duplicate detectors %s", ref)
		}
		out[ref] = true
	}
	return out, nil
}

// nonNegativeInt reads a JSON number that must be a non-negative integer. JSON
// has one number type, so a whole value arrives as a float64 and is only an
// integer if it survives the round trip.
func nonNegativeInt(raw any) (int, error) {
	f, ok := raw.(float64)
	if !ok {
		if i, isInt := raw.(int); isInt && i >= 0 {
			return i, nil
		}
		return 0, capErr("not an integer")
	}
	i := int(f)
	if float64(i) != f || i < 0 {
		return 0, capErr("not a non-negative integer")
	}
	return i, nil
}
