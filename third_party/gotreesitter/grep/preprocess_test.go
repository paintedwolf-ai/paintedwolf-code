package grep

import (
	"strings"
	"testing"
)

func TestPreprocess_NoMetavariables(t *testing.T) {
	pattern := "func main() error"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != pattern {
		t.Errorf("pattern = %q, want %q", got, pattern)
	}
	if len(mvars) != 0 {
		t.Errorf("len(mvars) = %d, want 0", len(mvars))
	}
}

func TestPreprocess_SingleCapture(t *testing.T) {
	pattern := "func $NAME() error"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "func __GREP_CAP_NAME__() error"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 1 {
		t.Fatalf("len(mvars) = %d, want 1", len(mvars))
	}

	mv, ok := mvars["__GREP_CAP_NAME__"]
	if !ok {
		t.Fatal("missing MetaVar for __GREP_CAP_NAME__")
	}
	if mv.Name != "NAME" {
		t.Errorf("Name = %q, want %q", mv.Name, "NAME")
	}
	if mv.Placeholder != "__GREP_CAP_NAME__" {
		t.Errorf("Placeholder = %q, want %q", mv.Placeholder, "__GREP_CAP_NAME__")
	}
	if mv.Variadic {
		t.Error("Variadic = true, want false")
	}
	if mv.Wildcard {
		t.Error("Wildcard = true, want false")
	}
	if mv.TypeConstraint != "" {
		t.Errorf("TypeConstraint = %q, want empty", mv.TypeConstraint)
	}
}

func TestPreprocess_MultipleSingleCaptures(t *testing.T) {
	pattern := "func $NAME($PARAM) $RET"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "func __GREP_CAP_NAME__(__GREP_CAP_PARAM__) __GREP_CAP_RET__"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 3 {
		t.Errorf("len(mvars) = %d, want 3", len(mvars))
	}

	for _, name := range []string{"NAME", "PARAM", "RET"} {
		ph := "__GREP_CAP_" + name + "__"
		mv, ok := mvars[ph]
		if !ok {
			t.Errorf("missing MetaVar for %s", ph)
			continue
		}
		if mv.Name != name {
			t.Errorf("MetaVar[%s].Name = %q, want %q", ph, mv.Name, name)
		}
	}
}

func TestPreprocess_VariadicCapture(t *testing.T) {
	pattern := "func $NAME($$$PARAMS) error"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "func __GREP_CAP_NAME__(__GREP_VAR_PARAMS__) error"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 2 {
		t.Fatalf("len(mvars) = %d, want 2", len(mvars))
	}

	mv, ok := mvars["__GREP_VAR_PARAMS__"]
	if !ok {
		t.Fatal("missing MetaVar for __GREP_VAR_PARAMS__")
	}
	if mv.Name != "PARAMS" {
		t.Errorf("Name = %q, want %q", mv.Name, "PARAMS")
	}
	if !mv.Variadic {
		t.Error("Variadic = false, want true")
	}
	if mv.Wildcard {
		t.Error("Wildcard = true, want false")
	}
}

func TestPreprocess_Wildcard(t *testing.T) {
	pattern := "func $_() error"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "func __GREP_WILD_1__() error"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 1 {
		t.Fatalf("len(mvars) = %d, want 1", len(mvars))
	}

	mv, ok := mvars["__GREP_WILD_1__"]
	if !ok {
		t.Fatal("missing MetaVar for __GREP_WILD_1__")
	}
	if mv.Name != "_" {
		t.Errorf("Name = %q, want %q", mv.Name, "_")
	}
	if !mv.Wildcard {
		t.Error("Wildcard = false, want true")
	}
	if mv.Variadic {
		t.Error("Variadic = true, want false")
	}
}

func TestPreprocess_MultipleWildcards(t *testing.T) {
	pattern := "$_ = $_ + $_"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "__GREP_WILD_1__ = __GREP_WILD_2__ + __GREP_WILD_3__"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 3 {
		t.Fatalf("len(mvars) = %d, want 3", len(mvars))
	}

	for i := 1; i <= 3; i++ {
		ph := "__GREP_WILD_" + string(rune('0'+i)) + "__"
		mv, ok := mvars[ph]
		if !ok {
			t.Errorf("missing MetaVar for %s", ph)
			continue
		}
		if !mv.Wildcard {
			t.Errorf("MetaVar[%s].Wildcard = false, want true", ph)
		}
	}
}

func TestPreprocess_TypedCapture(t *testing.T) {
	pattern := "func $NAME:identifier() error"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "func __GREP_TYPED_NAME__T__identifier__() error"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 1 {
		t.Fatalf("len(mvars) = %d, want 1", len(mvars))
	}

	mv, ok := mvars["__GREP_TYPED_NAME__T__identifier__"]
	if !ok {
		t.Fatal("missing MetaVar for __GREP_TYPED_NAME__T__identifier__")
	}
	if mv.Name != "NAME" {
		t.Errorf("Name = %q, want %q", mv.Name, "NAME")
	}
	if mv.TypeConstraint != "identifier" {
		t.Errorf("TypeConstraint = %q, want %q", mv.TypeConstraint, "identifier")
	}
	if mv.Variadic {
		t.Error("Variadic = true, want false")
	}
	if mv.Wildcard {
		t.Error("Wildcard = true, want false")
	}
}

func TestPreprocess_MixedMetavariables(t *testing.T) {
	pattern := "func $NAME:identifier($$$PARAMS) $_ { $_ }"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "func __GREP_TYPED_NAME__T__identifier__(__GREP_VAR_PARAMS__) __GREP_WILD_1__ { __GREP_WILD_2__ }"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 4 {
		t.Errorf("len(mvars) = %d, want 4", len(mvars))
	}
}

func TestPreprocess_ReservedPrefixError(t *testing.T) {
	pattern := "func __GREP_CAP_FOO__() error"
	_, _, err := Preprocess(pattern)
	if err == nil {
		t.Fatal("expected error for reserved prefix")
	}
	if !strings.Contains(err.Error(), "__GREP_") {
		t.Errorf("error = %q, want it to mention __GREP_", err.Error())
	}
}

func TestPreprocess_ReservedPrefixOnlyOutsideMetavariables(t *testing.T) {
	// The reserved prefix check should apply to the raw pattern text before
	// any substitutions. The string "__GREP_FOO" is suspicious.
	pattern := "var __GREP_FOO = $X"
	_, _, err := Preprocess(pattern)
	if err == nil {
		t.Fatal("expected error for reserved prefix in raw pattern")
	}
}

func TestPreprocess_RepeatedSameCapture(t *testing.T) {
	// The same metavariable appearing twice should produce the same
	// placeholder both times but only one map entry.
	pattern := "$X + $X"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "__GREP_CAP_X__ + __GREP_CAP_X__"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 1 {
		t.Errorf("len(mvars) = %d, want 1", len(mvars))
	}
}

func TestPreprocess_EmptyPattern(t *testing.T) {
	got, mvars, err := Preprocess("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("pattern = %q, want empty", got)
	}
	if len(mvars) != 0 {
		t.Errorf("len(mvars) = %d, want 0", len(mvars))
	}
}

func TestPreprocess_VariadicBareTripleDollar(t *testing.T) {
	// $$$ without a name is not a valid metavariable, should be left as is.
	pattern := "func main($$$) error"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// $$$ alone is not matched by the regex (requires a name after $$$),
	// so it stays unchanged.
	if got != pattern {
		t.Errorf("pattern = %q, want %q", got, pattern)
	}
	if len(mvars) != 0 {
		t.Errorf("len(mvars) = %d, want 0", len(mvars))
	}
}

func TestPreprocess_UnderscoreInName(t *testing.T) {
	pattern := "$my_var + $ANOTHER_VAR"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "__GREP_CAP_my_var__ + __GREP_CAP_ANOTHER_VAR__"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 2 {
		t.Errorf("len(mvars) = %d, want 2", len(mvars))
	}
}

func TestPreprocess_DollarFollowedByDigit(t *testing.T) {
	// $1 is not a valid metavariable (name must start with letter or _)
	pattern := "$1 + $2"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != pattern {
		t.Errorf("pattern = %q, want %q (unchanged)", got, pattern)
	}
	if len(mvars) != 0 {
		t.Errorf("len(mvars) = %d, want 0", len(mvars))
	}
}

func TestPreprocess_TypedCaptureWithUnderscoreType(t *testing.T) {
	pattern := "$EXPR:call_expression"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "__GREP_TYPED_EXPR__T__call_expression__"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}

	mv, ok := mvars[want]
	if !ok {
		t.Fatalf("missing MetaVar for %s", want)
	}
	if mv.TypeConstraint != "call_expression" {
		t.Errorf("TypeConstraint = %q, want %q", mv.TypeConstraint, "call_expression")
	}
}

func TestPreprocess_WildcardNotFollowedByWordChar(t *testing.T) {
	// $_ should be a wildcard even when followed by non-word chars.
	pattern := "$_.field"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "__GREP_WILD_1__.field"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 1 {
		t.Errorf("len(mvars) = %d, want 1", len(mvars))
	}
}

func TestPreprocess_ComplexRealWorldPattern(t *testing.T) {
	// A realistic Go pattern
	pattern := "func $NAME:identifier($$$PARAMS) ($RET, error) { $$$BODY }"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "func __GREP_TYPED_NAME__T__identifier__(__GREP_VAR_PARAMS__) (__GREP_CAP_RET__, error) { __GREP_VAR_BODY__ }"
	if got != want {
		t.Errorf("pattern = %q\n   want %q", got, want)
	}
	if len(mvars) != 4 {
		t.Errorf("len(mvars) = %d, want 4", len(mvars))
	}

	// Verify variadic flags
	if mv := mvars["__GREP_VAR_PARAMS__"]; mv == nil || !mv.Variadic {
		t.Error("PARAMS should be variadic")
	}
	if mv := mvars["__GREP_VAR_BODY__"]; mv == nil || !mv.Variadic {
		t.Error("BODY should be variadic")
	}
}

func TestPreprocess_ReservedPrefixInStringLiteral(t *testing.T) {
	// Even inside what looks like a string literal, the reserved prefix
	// should trigger an error. We're not parsing the host language so we
	// can't distinguish string content from code.
	pattern := `fmt.Println("__GREP_CAP_X__")`
	_, _, err := Preprocess(pattern)
	if err == nil {
		t.Fatal("expected error for reserved prefix inside pattern")
	}
}

func TestPreprocess_WildcardAmongCaptures(t *testing.T) {
	// Wildcards should be numbered independently per occurrence.
	pattern := "$X = $_; $Y = $_"
	got, mvars, err := Preprocess(pattern)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "__GREP_CAP_X__ = __GREP_WILD_1__; __GREP_CAP_Y__ = __GREP_WILD_2__"
	if got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	if len(mvars) != 4 {
		t.Errorf("len(mvars) = %d, want 4", len(mvars))
	}
}
