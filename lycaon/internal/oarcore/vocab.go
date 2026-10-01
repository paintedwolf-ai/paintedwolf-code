// Package oarcore implements Open Agent Rules 1.0 core evaluation.
package oarcore

// This file transcribes the vendored schemas/oar/vocabulary.yaml;
// TestVocabularyMatchesPublished fails when the two drift.

// FactType is a declarable fact type ([OAR-FACT-22]). An engine MUST NOT declare
// a fact of any other type.
type FactType string

// The complete set of declarable fact types.
const (
	TypeBool       FactType = "bool"
	TypeInt        FactType = "int"
	TypeDouble     FactType = "double"
	TypeString     FactType = "string"
	TypeListString FactType = "list<string>"
	TypeMapString  FactType = "map<string,string>"
	TypeListMap    FactType = "list<map>"
	TypeMap        FactType = "map"
)

var factTypes = []FactType{
	TypeBool, TypeInt, TypeDouble, TypeString,
	TypeListString, TypeMapString, TypeListMap, TypeMap,
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
// ([OAR-FACT-9]).
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

// coreFacts is the core tier ([OAR-FACT-15]): small, holding only
// what is meaningful at every anchor of every host — the moment itself, and the
// engine's own counters.
//
// fire_count counts declared increments, not firings: a rule that fires at every
// occurrence and declares no on_fire has a fire_count of zero.
var coreFacts = map[string]FactType{
	"anchor":        TypeString,
	"fire_count":    TypeInt,
	"breaker_count": TypeInt,
}

// coreFunctions are the two core-tier observation functions that read the
// counters of *another* rule ([OAR-FIRE-11]). They are core, so a rule reaches
// them without naming anything in requires.
var coreFunctions = map[string]FunctionSig{
	"fire_count_of":    {Arg: TypeString, Ret: TypeInt},
	"breaker_count_of": {Arg: TypeString, Ret: TypeInt},
}

// counterReaders maps each core counter-reading function to the counter it reads
// ([OAR-FIRE-3]).
var counterReaders = map[string]string{
	"fire_count_of":    "fire_count",
	"breaker_count_of": "breaker_count",
}

// reservedWords are the three words that are never an identifier
// ([OAR-EXPR-24]). An engine MUST NOT declare a fact or an observation function
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

// stringBuiltins are the three (string, string) -> bool built-ins
// ([OAR-EXPR-22]).
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
// at all ([OAR-FACT-16]).
type ProfileDef struct {
	Facts     map[string]FactType
	Functions map[string]FunctionSig
}

// profiles is the standard profile vocabulary of section 5.4.
var profiles = map[string]ProfileDef{
	"tool": {
		Facts: map[string]FactType{
			"tool":                  TypeString,
			"tool_args":             TypeMap,
			"tool_args_fingerprint": TypeString,
			"arg_validation_errors": TypeListString,
			"arg_validation_reason": TypeString,
			"arg_validation_field":  TypeString,
			"permission_profile":    TypeString,
			"policy_denied":         TypeBool,
		},
		// tool_args is an unparameterised map and may not be indexed
		// ([OAR-EXPR-19]); its members are reached through these typed accessors,
		// so a condition over them stays statically checkable.
		Functions: map[string]FunctionSig{
			"tool_arg_string": {Arg: TypeString, Ret: TypeString},
			"tool_arg_int":    {Arg: TypeString, Ret: TypeInt},
			"tool_arg_bool":   {Arg: TypeString, Ret: TypeBool},
		},
	},
	"session": {
		Facts: map[string]FactType{
			"session_posture": TypeString,
			"principal":       TypeString,
			"principal_roles": TypeListString,
		},
		Functions: map[string]FunctionSig{},
	},
	"filesystem": {
		Facts: map[string]FactType{
			"is_directory": TypeBool,
			"not_found":    TypeBool,
			"path_denied":  TypeBool,
		},
		Functions: map[string]FunctionSig{
			"path_outside_scope": {Arg: TypeString, Ret: TypeBool},
		},
	},
	// A host that preserves structured content segments and their provenance.
	// The five list facts are positionally aligned with one another and with
	// content_segment_count, so a condition may read a segment's role beside its
	// origin without a second lookup.
	"content-provenance": {
		Facts: map[string]FactType{
			"content_roles":              TypeListString,
			"content_origins":            TypeListString,
			"content_authorities":        TypeListString,
			"content_trust_tiers":        TypeListString,
			"content_sources":            TypeListString,
			"content_segment_count":      TypeInt,
			"content_contains_untrusted": TypeBool,
		},
		Functions: map[string]FunctionSig{},
	},
	"content":          {Facts: map[string]FactType{"content_length": TypeInt}, Functions: map[string]FunctionSig{}},
	"secrets":          {Facts: map[string]FactType{"secret_matches": TypeListMap}, Functions: map[string]FunctionSig{}},
	"pii":              {Facts: map[string]FactType{"pii_entities": TypeListMap}, Functions: map[string]FunctionSig{}},
	"prompt-injection": {Facts: map[string]FactType{"prompt_injection_score": TypeDouble}, Functions: map[string]FunctionSig{}},
	"jailbreak":        {Facts: map[string]FactType{"jailbreak_score": TypeDouble}, Functions: map[string]FunctionSig{}},
	"moderation": {
		Facts: map[string]FactType{
			"moderation_categories": TypeListString,
		},
		// A classifier scores content against named categories; the score for one
		// is reached through a declared function rather than by indexing, so the
		// condition stays statically checkable ([OAR-EXPR-19]).
		Functions: map[string]FunctionSig{
			"moderation_score": {Arg: TypeString, Ret: TypeDouble},
		},
	},
	"mcp": {
		Facts: map[string]FactType{
			"mcp_provider_id":         TypeString,
			"mcp_tool_name":           TypeString,
			"mcp_qualified_tool":      TypeString,
			"mcp_provider_configured": TypeBool,
			"mcp_provider_enabled":    TypeBool,
			"mcp_call_ok":             TypeBool,
			"mcp_error_code":          TypeString,
			"mcp_schema_matched":      TypeBool,
		},
		// The parameterized forms carry the _for suffix because a fact and a
		// function may not share a name ([OAR-FACT-2]).
		Functions: map[string]FunctionSig{
			"mcp_provider_configured_for": {Arg: TypeString, Ret: TypeBool},
			"mcp_provider_enabled_for":    {Arg: TypeString, Ret: TypeBool},
			"mcp_has_field":               {Arg: TypeString, Ret: TypeBool},
			"mcp_field_bool":              {Arg: TypeString, Ret: TypeBool},
			"mcp_field_string":            {Arg: TypeString, Ret: TypeString},
			"mcp_field_int":               {Arg: TypeString, Ret: TypeInt},
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
// ([OAR-CONF-25]). None of them requires a model or a network.
var reservedDetectors = []string{
	"detector://noop",
	"detector://error",
	"detector://fixture",
}

var kinds = []string{"schema", "policy", "invariant", "detector"}

var (
	effects          = []string{"block", "warn", "nudge", "allow", "transform"}
	enforcements     = []string{"enforce", "monitor", "off"}
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
