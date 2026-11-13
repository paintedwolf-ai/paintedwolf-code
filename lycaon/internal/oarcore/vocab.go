// Package oarcore implements Open Agent Rules 1.0 core evaluation.
package oarcore

// This file transcribes the vendored schemas/oar/vocabulary.yaml;
// TestVocabularyMatchesPublished fails when the two drift.

// FactType is a declarable fact type. The vocabulary is closed
// ([OAR-FACT-16]): an engine MUST NOT declare a fact of any other type.
type FactType string

// The complete set of declarable fact types.
const (
	TypeBool       FactType = "bool"
	TypeInt        FactType = "int"
	TypeDouble     FactType = "double"
	TypeString     FactType = "string"
	TypeListString FactType = "list<string>"
	// TypeMap holds dynamically typed members ([OAR-FACT-22]).
	TypeMap     FactType = "map<string, dyn>"
	TypeListMap FactType = "list<map<string, dyn>>"
)

var factTypes = []FactType{
	TypeBool, TypeInt, TypeDouble, TypeString, TypeListString, TypeMap, TypeListMap,
}

func isFactType(s string) bool {
	for _, t := range factTypes {
		if string(t) == s {
			return true
		}
	}
	return false
}

// FunctionSig is an observation function: one argument, one result
// ([OAR-FACT-7]).
type FunctionSig struct {
	Arg FactType
	Ret FactType
}

// coreAnchors are the eight lifecycle moments the standard fixes, in
// specification order ([OAR-PROF-1]). A host MUST NOT redefine them.
var coreAnchors = []string{
	"tool.pre_invoke",
	"tool.handler",
	"tool.post_invoke",
	"agent.post_turn",
	"agent.finalize",
	"model.input",
	"model.output",
	"model.tool_result",
}

// IsCoreAnchor reports whether name is one of the eight core anchors.
func IsCoreAnchor(name string) bool {
	for _, a := range coreAnchors {
		if a == name {
			return true
		}
	}
	return false
}

// coreFacts is the core tier ([OAR-FACT-12], section 5.2): what every
// conforming engine provides at every anchor.
var coreFacts = map[string]FactType{
	"tool":                 TypeString,
	"tool_args":            TypeMap,
	"fire_count":           TypeInt,
	"breaker_count":        TypeInt,
	"same_code_reject_run": TypeInt,
	"same_call_run":        TypeInt,
}

// EngineMaintained are the core facts the engine computes per session itself;
// no fact provider supplies them ([OAR-FIRE-10]).
var EngineMaintained = []string{"fire_count", "breaker_count", "same_code_reject_run", "same_call_run"}

// IsEngineMaintained reports whether name is a fact the engine maintains.
func IsEngineMaintained(name string) bool { return contains(EngineMaintained, name) }

// coreFunctions are the core observation functions (section 5.3). Each reads
// another rule's counter, named by a string literal ([OAR-FIRE-6]).
var coreFunctions = map[string]FunctionSig{
	"fire_count_of":    {Arg: TypeString, Ret: TypeInt},
	"breaker_count_of": {Arg: TypeString, Ret: TypeInt},
}

// counterReaders maps each core counter reader to the counter it reads.
var counterReaders = map[string]string{
	"fire_count_of":    "fire_count",
	"breaker_count_of": "breaker_count",
}

// reservedWords are the three words that are never an identifier.
// An engine MUST NOT declare a fact or an observation function
// under one of them.
var reservedWords = []string{"true", "false", "in"}

func isReservedWord(name string) bool {
	for _, w := range reservedWords {
		if w == name {
			return true
		}
	}
	return false
}

// stringBuiltins are the three (string, string) -> bool built-ins.
var stringBuiltins = []string{"starts_with", "ends_with", "contains"}

// builtins are the four built-ins of Appendix C; no fact or function may take
// one of their names ([OAR-EXPR-17]).
var builtins = []string{"size", "starts_with", "ends_with", "contains"}

// IsReservedName reports whether a fact or function may not use name.
func IsReservedName(name string) bool { return isReservedWord(name) || contains(builtins, name) }

func isStringBuiltin(name string) bool {
	for _, b := range stringBuiltins {
		if b == name {
			return true
		}
	}
	return false
}

// ProfileDef is one named capability profile. A profile is provided whole or not
// at all ([OAR-FACT-13]).
type ProfileDef struct {
	Facts     map[string]FactType
	Functions map[string]FunctionSig
}

// profiles is the standard profile vocabulary of section 5.4. A profile is
// provided whole or not at all ([OAR-FACT-13]).
var profiles = map[string]ProfileDef{
	"filesystem": {
		Facts: map[string]FactType{
			"is_directory":       TypeBool,
			"not_found":          TypeBool,
			"path_outside_scope": TypeBool,
		},
		Functions: map[string]FunctionSig{},
	},
	"content-safety": {
		Facts: map[string]FactType{
			"prompt_injection_score": TypeDouble,
			"jailbreak_score":        TypeDouble,
			"pii_entities":           TypeListMap,
			"secret_matches":         TypeListMap,
		},
		Functions: map[string]FunctionSig{},
	},
	"mcp": {
		Facts: map[string]FactType{
			"mcp_server_id":          TypeString,
			"mcp_tool_name":          TypeString,
			"mcp_qualified_tool":     TypeString,
			"mcp_server_configured":  TypeBool,
			"mcp_server_enabled":     TypeBool,
			"mcp_configured_servers": TypeListString,
			"mcp_enabled_servers":    TypeListString,
			"mcp_call_ok":            TypeBool,
			"mcp_error_code":         TypeString,
			"mcp_schema_matched":     TypeBool,
			"mcp_result":             TypeMap,
		},
		// The accessors reach the members of mcp_result that selection cannot
		// type; a string member is read by selection.
		Functions: map[string]FunctionSig{
			"mcp_field_bool": {Arg: TypeString, Ret: TypeBool},
			"mcp_field_int":  {Arg: TypeString, Ret: TypeInt},
		},
	},
}

// onFireActions is the closed side-effect vocabulary ([OAR-FIRE-1]).
var onFireActions = []string{
	"increment_counter",
	"reset_counter",
	"publish_event",
	"increment_breaker",
	"reset_breaker",
}

// onFireWrites binds each counter action to exactly one core fact
// ([OAR-FIRE-3]). publish_event writes no fact.
var onFireWrites = map[string]string{
	"increment_counter": "fire_count",
	"reset_counter":     "fire_count",
	"increment_breaker": "breaker_count",
	"reset_breaker":     "breaker_count",
	"publish_event":     "",
}

// reservedDetectors are the three detector references the corpus reserves
// ([OAR-CONF-18]). None of them requires a model or a network.
var reservedDetectors = []string{
	"detector://noop",
	"detector://error",
	"detector://fixture",
}

var kinds = []string{"schema", "policy", "invariant", "detector"}

var (
	effects      = []string{"block", "warn", "nudge", "allow"}
	enforcements = []string{"enforce", "monitor"}
)

// The format version this implementation supports.
const (
	oarMajor = 1
	oarMinor = 0
)

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// CoreFacts returns the core fact names and types.
func CoreFacts() map[string]FactType {
	out := make(map[string]FactType, len(coreFacts))
	for name, typ := range coreFacts {
		out[name] = typ
	}
	return out
}

// CoreFunctions returns the core observation functions.
func CoreFunctions() map[string]FunctionSig {
	out := make(map[string]FunctionSig, len(coreFunctions))
	for name, sig := range coreFunctions {
		out[name] = sig
	}
	return out
}

// StandardProfiles returns each standard profile's facts and functions.
func StandardProfiles() map[string]ProfileDef {
	out := make(map[string]ProfileDef, len(profiles))
	for name, def := range profiles {
		copied := ProfileDef{Facts: map[string]FactType{}, Functions: map[string]FunctionSig{}}
		for fact, typ := range def.Facts {
			copied.Facts[fact] = typ
		}
		for fn, sig := range def.Functions {
			copied.Functions[fn] = sig
		}
		out[name] = copied
	}
	return out
}
