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
	"tool":                  TypeString,
	"tool_args":             TypeMap,
	"session_posture":       TypeString,
	"profile":               TypeString,
	"arg_validation_errors": TypeListString,
	"tool_args_fingerprint": TypeString,
	"breaker_count":         TypeInt,
	"same_code_reject_run":  TypeInt,
	"is_directory":          TypeBool,
	"not_found":             TypeBool,
	"path_denied":           TypeBool,
	"policy_denied":         TypeBool,
}

// coreFunctions are the core observation functions (section 5.3).
var coreFunctions = map[string]FunctionSig{
	"path_outside_scope": {Arg: TypeString, Ret: TypeBool},
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
			"mcp_server_id":         TypeString,
			"mcp_tool_name":         TypeString,
			"mcp_qualified_tool":    TypeString,
			"mcp_server_configured": TypeBool,
			"mcp_server_enabled":    TypeBool,
			"mcp_call_ok":           TypeBool,
			"mcp_error_code":        TypeString,
			"mcp_schema_matched":    TypeBool,
		},
		// The parameterized forms carry the _for suffix because a fact and a
		// function may not share a name ([OAR-FACT-19]).
		Functions: map[string]FunctionSig{
			"mcp_server_configured_for": {Arg: TypeString, Ret: TypeBool},
			"mcp_server_enabled_for":    {Arg: TypeString, Ret: TypeBool},
			"mcp_has_field":             {Arg: TypeString, Ret: TypeBool},
			"mcp_field_bool":            {Arg: TypeString, Ret: TypeBool},
			"mcp_field_string":          {Arg: TypeString, Ret: TypeString},
			"mcp_field_int":             {Arg: TypeString, Ret: TypeInt},
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
	"increment_counter": "",
	"reset_counter":     "",
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
	effects          = []string{"block", "warn", "nudge", "allow", "transform"}
	enforcements     = []string{"enforce", "monitor"}
	statuses         = []string{"experimental", "test", "stable", "deprecated"}
	relatedTypes     = []string{"derived", "obsolete", "merged", "renamed", "similar"}
	transformActions = []string{"redact", "replace", "annotate"}
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
