package oarcore

// The anchor profile map, and the fact environment an engine declares.
//
// [OAR-PROF-2] A host publishes an anchor profile map naming, for each core
// anchor, the local anchor that implements it or that it is unsupported.
// [OAR-FACT-1] The fact environment is closed and typed and declared whole
// before any rule is compiled: the core tier, every standard profile the
// engine claims, and the host tier under the host's own namespace.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// EnvironmentError is a profile map or environment declaration this engine
// refuses.
type EnvironmentError struct{ Message string }

func (e *EnvironmentError) Error() string { return e.Message }

func envErr(format string, args ...any) error {
	return &EnvironmentError{Message: fmt.Sprintf(format, args...)}
}

// Tier is where a declared name lives ([OAR-FACT-11]).
type Tier string

const (
	TierCore     Tier = "core"
	TierStandard Tier = "standard"
	TierHost     Tier = "host"
)

// FactDecl is one declared fact.
type FactDecl struct {
	Type FactType
	Tier Tier
	// Profile names the standard profile a standard-tier fact belongs to.
	Profile string
}

// FunctionDecl is one declared observation function.
type FunctionDecl struct {
	Sig  FunctionSig
	Tier Tier
}

// Environment is the closed, typed environment rules compile against.
type Environment struct {
	// Anchors maps a core anchor identifier to the local anchor implementing it.
	Anchors map[string]string
	// HostAnchors is the host's catalogue of native anchors ([OAR-PROF-4]).
	HostAnchors        map[string]bool
	Profiles           map[string]bool
	Facts              map[string]FactDecl
	Functions          map[string]FunctionDecl
	ActivityWindow     int
	Detectors          map[string]bool
	ExpressionNodesMax int
	SupportsTransform  bool
	// SupportsChunk reports streamed evaluation of content anchors
	// ([OAR-OPS-6]).
	SupportsChunk bool
}

var (
	namespaceRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	localAnchorRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	fnSigRE       = regexp.MustCompile(`^\((bool|int|double|string)\) -> (bool|int|double|string)$`)
	detectorRE    = regexp.MustCompile(`^detector://[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// Each version part is a decimal integer with no leading zero: "01.0" is
	// not a version.
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// ProfileMap is a parsed anchor profile map ([OAR-PROF-2]).
type ProfileMap struct {
	Core        map[string]string
	Unsupported []string
}

// LoadProfileMap validates an anchor profile map document
// (oar-profile.schema.json). Every core anchor appears exactly once across
// core and unsupported.
func LoadProfileMap(doc any) (ProfileMap, error) {
	obj, ok := asObject(doc)
	if !ok {
		return ProfileMap{}, envErr("anchor profile map is not an object")
	}
	for key := range obj {
		if key != "oar_profile_version" && key != "core" && key != "unsupported" {
			return ProfileMap{}, envErr("anchor profile map carries undefined field %q", key)
		}
	}
	version, ok := obj["oar_profile_version"].(string)
	if !ok || !versionRE.MatchString(version) {
		return ProfileMap{}, envErr("anchor profile map field \"oar_profile_version\" must be <major>.<minor>")
	}
	if !strings.HasPrefix(version, fmt.Sprintf("%d.", oarMajor)) {
		return ProfileMap{}, envErr("anchor profile map targets OAR %s, not %d.%d", version, oarMajor, oarMinor)
	}
	coreDoc, ok := asObject(obj["core"])
	if !ok {
		return ProfileMap{}, envErr("anchor profile map field \"core\" must be an object")
	}
	out := ProfileMap{Core: make(map[string]string, len(coreDoc))}
	for core, localRaw := range coreDoc {
		if !IsCoreAnchor(core) {
			return ProfileMap{}, envErr("anchor profile map names %q, which is not a core anchor", core)
		}
		local, ok := localRaw.(string)
		if !ok || !localAnchorRE.MatchString(local) {
			return ProfileMap{}, envErr("anchor profile map binds %s to a malformed local anchor", core)
		}
		out.Core[core] = local
	}
	list, ok := obj["unsupported"].([]any)
	if !ok {
		return ProfileMap{}, envErr("anchor profile map field \"unsupported\" must be a list")
	}
	for _, entry := range list {
		name, ok := entry.(string)
		if !ok || !IsCoreAnchor(name) {
			return ProfileMap{}, envErr("anchor profile map lists %v as unsupported, which is not a core anchor", entry)
		}
		if contains(out.Unsupported, name) {
			return ProfileMap{}, envErr("[OAR-PROF-2] anchor profile map lists %s as unsupported twice", name)
		}
		out.Unsupported = append(out.Unsupported, name)
	}
	for _, core := range coreAnchors {
		_, inCore := out.Core[core]
		inUnsupported := contains(out.Unsupported, core)
		if inCore && inUnsupported {
			return ProfileMap{}, envErr("[OAR-PROF-2] anchor profile map declares core anchor %s twice", core)
		}
		if !inCore && !inUnsupported {
			return ProfileMap{}, envErr("[OAR-PROF-2] anchor profile map omits core anchor %s", core)
		}
	}
	return out, nil
}

// EnvironmentSpec is everything an engine declares about itself.
type EnvironmentSpec struct {
	// Host is the namespace host-tier facts are published under
	// ([OAR-FACT-15]).
	Host        string
	ProfileMap  ProfileMap
	HostAnchors []string
	// Profiles are the standard profiles the engine claims; each is provided
	// whole ([OAR-FACT-13]).
	Profiles []string
	// HostFacts declares host-tier facts and functions by name and type
	// spelling: a fact type, or "(arg) -> ret" for a function.
	HostFacts          map[string]string
	ActivityWindow     int
	Detectors          []string
	ExpressionNodesMax int
	SupportsTransform  bool
	SupportsChunk      bool
}

// NewEnvironment declares the closed fact environment.
func NewEnvironment(spec EnvironmentSpec) (*Environment, error) {
	if !namespaceRE.MatchString(spec.Host) {
		return nil, envErr("host %q is not a namespace", spec.Host)
	}
	env := &Environment{
		Anchors:            map[string]string{},
		HostAnchors:        map[string]bool{},
		Profiles:           map[string]bool{},
		Facts:              map[string]FactDecl{},
		Functions:          map[string]FunctionDecl{},
		ActivityWindow:     spec.ActivityWindow,
		Detectors:          map[string]bool{},
		ExpressionNodesMax: spec.ExpressionNodesMax,
		SupportsTransform:  spec.SupportsTransform,
		SupportsChunk:      spec.SupportsChunk,
	}
	for core, local := range spec.ProfileMap.Core {
		env.Anchors[core] = local
	}
	for _, name := range spec.HostAnchors {
		if !localAnchorRE.MatchString(name) || IsCoreAnchor(name) {
			return nil, envErr("[OAR-PROF-4] host anchor %q is malformed or collides with a core anchor", name)
		}
		env.HostAnchors[name] = true
	}
	for name, typ := range coreFacts {
		env.Facts[name] = FactDecl{Type: typ, Tier: TierCore}
	}
	for name, sig := range coreFunctions {
		env.Functions[name] = FunctionDecl{Sig: sig, Tier: TierCore}
	}
	for _, name := range spec.Profiles {
		def, known := profiles[name]
		if !known {
			return nil, envErr("[OAR-FACT-13] engine claims profile %q, which the specification does not define", name)
		}
		env.Profiles[name] = true
		for fact, typ := range def.Facts {
			env.Facts[fact] = FactDecl{Type: typ, Tier: TierStandard, Profile: name}
		}
		for fn, sig := range def.Functions {
			env.Functions[fn] = FunctionDecl{Sig: sig, Tier: TierStandard}
		}
	}
	names := make([]string, 0, len(spec.HostFacts))
	for name := range spec.HostFacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := declareHostFact(env, spec.Host, name, spec.HostFacts[name]); err != nil {
			return nil, err
		}
	}
	for _, ref := range spec.Detectors {
		if !detectorRE.MatchString(ref) {
			return nil, envErr("[OAR-OPS-10] malformed detector reference %q", ref)
		}
		env.Detectors[ref] = true
	}
	return env, nil
}

// declareHostFact adds one host-tier fact or function. It is published under
// the host's namespace ([OAR-FACT-15]), never redefines a core or standard
// name ([OAR-FACT-14]), never shares a name with another declaration
// ([OAR-FACT-19]), and has a type of the closed vocabulary ([OAR-FACT-16]).
func declareHostFact(env *Environment, host, name, typ string) error {
	if !strings.HasPrefix(name, host+".") || !factNameRE.MatchString(name) {
		return envErr("[OAR-FACT-15] host fact %s is not published under the host namespace %s", name, host)
	}
	if isReservedWord(name) {
		return envErr("host fact %s is declared under a reserved word", name)
	}
	_, isFact := env.Facts[name]
	_, isFn := env.Functions[name]
	if isFact || isFn {
		return envErr("[OAR-FACT-19] host fact %s redefines a declared name", name)
	}
	if m := fnSigRE.FindStringSubmatch(typ); m != nil {
		env.Functions[name] = FunctionDecl{Sig: FunctionSig{Arg: FactType(m[1]), Ret: FactType(m[2])}, Tier: TierHost}
		return nil
	}
	if !isFactType(typ) {
		return envErr("[OAR-FACT-16] host fact %s declares type %q, which is outside the closed type vocabulary", name, typ)
	}
	env.Facts[name] = FactDecl{Type: FactType(typ), Tier: TierHost}
	return nil
}
