package gojq

import "strconv"

// ValueError is an interface for errors with a value for internal function.
// Return an error implementing this interface when you want to catch error
// values (not error messages) by try-catch, just like built-in error function.
// Refer to [WithFunction] to add a custom internal function.
type ValueError interface {
	error
	Value() any
}

// expectedObjectError and expectedArrayError report an index whose key the
// value cannot take, in jq's words. key is the index when the site knows it.
type expectedObjectError struct {
	v, key any
}

func (err *expectedObjectError) Error() string {
	return indexErrorMessage(err.v, err.key, "string")
}

type expectedArrayError struct {
	v, key any
}

func (err *expectedArrayError) Error() string {
	return indexErrorMessage(err.v, err.key, "number")
}

func indexErrorMessage(v, key any, fallback string) string {
	with := fallback
	switch k := key.(type) {
	case nil:
	case string:
		with = "string " + jsonMarshal(k)
	default:
		with = TypeOf(k)
	}
	return "Cannot index " + TypeOf(v) + " with " + with
}

type iteratorError struct {
	v any
}

func (err *iteratorError) Error() string {
	return "Cannot iterate over " + typeErrorPreview(err.v)
}

type arrayIndexNegativeError struct {
	v int
}

func (err *arrayIndexNegativeError) Error() string {
	return "Out of bounds negative array index"
}

type arrayIndexTooLargeError struct {
	v any
}

func (err *arrayIndexTooLargeError) Error() string {
	return "array index too large: " + Preview(err.v)
}

type repeatStringTooLargeError struct {
	s string
	n float64
}

func (err *repeatStringTooLargeError) Error() string {
	return "repeat string result too large: " + Preview(err.s) + " * " + Preview(err.n)
}

type objectKeyNotStringError struct {
	v any
}

func (err *objectKeyNotStringError) Error() string {
	return "expected a string for object key but got: " + typeErrorPreview(err.v)
}

type arrayIndexNotNumberError struct {
	v any
}

func (err *arrayIndexNotNumberError) Error() string {
	return "Array/string slice indices must be integers"
}

type stringIndexNotNumberError struct {
	v any
}

func (err *stringIndexNotNumberError) Error() string {
	return "Array/string slice indices must be integers"
}

// expectedStartEndError is an object index without slice bounds on v.
type expectedStartEndError struct {
	v any
}

func (err *expectedStartEndError) Error() string {
	return "Cannot index " + TypeOf(err.v) + " with object"
}

type lengthMismatchError struct{}

func (*lengthMismatchError) Error() string {
	return "length mismatch"
}

type inputNotAllowedError struct{}

func (*inputNotAllowedError) Error() string {
	return "input(s)/0 is not allowed"
}

type funcNotFoundError struct {
	f *Func
}

func (err *funcNotFoundError) Error() string {
	return "function not defined: " + err.f.Name + "/" + strconv.Itoa(len(err.f.Args))
}

type func0TypeError struct {
	name string
	v    any
}

func (err *func0TypeError) Error() string {
	switch err.name {
	case "length":
		return typeErrorPreview(err.v) + " has no length"
	case "keys", "keys_unsorted":
		return typeErrorPreview(err.v) + " has no keys"
	case "sort":
		return typeErrorPreview(err.v) + " cannot be sorted, as it is not an array"
	case "abs":
		return typeErrorPreview(err.v) + " cannot be negated"
	case "@csv", "@tsv":
		return typeErrorPreview(err.v) + " cannot be " + err.name[1:] + "-formatted, only array"
	}
	return err.name + " cannot be applied to: " + typeErrorPreview(err.v)
}

type func1TypeError struct {
	name string
	v, w any
}

func (err *func1TypeError) Error() string {
	if err.name == "has" {
		return "Cannot check whether " + TypeOf(err.v) + " has a " + TypeOf(err.w) + " key"
	}
	if matchFunctions[err.name] {
		return typeErrorPreview(err.v) + " cannot be matched, as it is not a string"
	}
	return err.name + "(" + Preview(err.w) + ") cannot be applied to: " + typeErrorPreview(err.v)
}

type func2TypeError struct {
	name    string
	v, w, x any
}

// matchFunctions are the regex builtins jq reports as one match failure.
var matchFunctions = map[string]bool{"test": true, "match": true, "capture": true, "scan": true, "splits": true, "sub": true, "gsub": true}

func (err *func2TypeError) Error() string {
	if matchFunctions[err.name] {
		return typeErrorPreview(err.v) + " cannot be matched, as it is not a string"
	}
	return err.name + "(" + Preview(err.w) + "; " + Preview(err.x) + ") cannot be applied to: " + typeErrorPreview(err.v)
}

type func0WrapError struct {
	name string
	v    any
	err  error
}

func (err *func0WrapError) Error() string {
	if err.name == "tonumber" {
		return typeErrorPreview(err.v) + " cannot be parsed as a number"
	}
	return err.name + " cannot be applied to " + Preview(err.v) + ": " + err.err.Error()
}

type func1WrapError struct {
	name string
	v, w any
	err  error
}

func (err *func1WrapError) Error() string {
	if pathFunctions[err.name] {
		return err.err.Error()
	}
	return err.name + "(" + Preview(err.w) + ") cannot be applied to " + Preview(err.v) + ": " + err.err.Error()
}

type func2WrapError struct {
	name    string
	v, w, x any
	err     error
}

// pathFunctions report the failing step itself, as jq does.
var pathFunctions = map[string]bool{"getpath": true, "setpath": true, "delpaths": true}

func (err *func2WrapError) Error() string {
	if pathFunctions[err.name] {
		return err.err.Error()
	}
	return err.name + "(" + Preview(err.w) + "; " + Preview(err.x) + ") cannot be applied to " + Preview(err.v) + ": " + err.err.Error()
}

type exitCodeError struct {
	value any
	code  int
}

func (err *exitCodeError) Error() string {
	if s, ok := err.value.(string); ok {
		return "error: " + s
	}
	return "error: " + jsonMarshal(err.value)
}

func (err *exitCodeError) Value() any {
	return err.value
}

func (err *exitCodeError) ExitCode() int {
	return err.code
}

// HaltError is an error emitted by halt and halt_error functions.
// It implements [ValueError], and if the value is nil, discard the error
// and stop the iteration. Consider a query like "1, halt, 2";
// the first value is 1, and the second value is a HaltError with nil value.
// You might think the iterator should not emit an error this case, but it
// should so that we can recognize the halt error to stop the outer loop
// of iterating input values; echo 1 2 3 | gojq "., halt".
type HaltError exitCodeError

func (err *HaltError) Error() string {
	return "halt " + (*exitCodeError)(err).Error()
}

// Value returns the value of the error. This implements [ValueError],
// but halt error is not catchable by try-catch.
func (err *HaltError) Value() any {
	return (*exitCodeError)(err).Value()
}

// ExitCode returns the exit code of the error.
func (err *HaltError) ExitCode() int {
	return (*exitCodeError)(err).ExitCode()
}

type flattenDepthError struct {
	v float64
}

func (err *flattenDepthError) Error() string {
	return "flatten depth should not be negative: " + Preview(err.v)
}

type timeArrayError struct{}

func (*timeArrayError) Error() string {
	return "expected an array of 8 numbers"
}

type unaryTypeError struct {
	name string
	v    any
}

func (err *unaryTypeError) Error() string {
	if err.name == "negate" {
		return typeErrorPreview(err.v) + " cannot be negated"
	}
	return "cannot " + err.name + ": " + typeErrorPreview(err.v)
}

type binopTypeError struct {
	name string
	l, r any
}

var binopVerbs = map[string]string{
	"add": "added", "subtract": "subtracted", "multiply": "multiplied",
	"divide": "divided", "modulo": "divided (remainder)",
}

func (err *binopTypeError) Error() string {
	return typeErrorPreview(err.l) + " and " + typeErrorPreview(err.r) + " cannot be " + binopVerbs[err.name]
}

type zeroDivisionError struct {
	l, r any
}

func (err *zeroDivisionError) Error() string {
	return typeErrorPreview(err.l) + " and " + typeErrorPreview(err.r) + " cannot be divided because the divisor is zero"
}

type zeroModuloError struct {
	l, r any
}

func (err *zeroModuloError) Error() string {
	return typeErrorPreview(err.l) + " and " + typeErrorPreview(err.r) + " cannot be divided (remainder) because the divisor is zero"
}

type formatNotFoundError struct {
	n string
}

func (err *formatNotFoundError) Error() string {
	return "format not defined: " + err.n
}

type formatRowError struct {
	typ string
	v   any
}

func (err *formatRowError) Error() string {
	return typeErrorPreview(err.v) + " is not valid in a csv row"
}

type tooManyVariableValuesError struct{}

func (*tooManyVariableValuesError) Error() string {
	return "too many variable values provided"
}

type expectedVariableError struct {
	n string
}

func (err *expectedVariableError) Error() string {
	return "variable defined but not bound: " + err.n
}

type variableNotFoundError struct {
	n string
}

func (err *variableNotFoundError) Error() string {
	return "variable not defined: " + err.n
}

type variableNameError struct {
	n string
}

func (err *variableNameError) Error() string {
	return "invalid variable name: " + err.n
}

type breakError struct {
	n string
	v any
}

func (err *breakError) Error() string {
	return "label not defined: " + err.n
}

func (*breakError) ExitCode() int {
	return 3
}

type tryEndError struct {
	err error
}

func (err *tryEndError) Error() string {
	return err.err.Error()
}

type invalidPathError struct {
	v any
}

func (err *invalidPathError) Error() string {
	return "Invalid path expression with result " + truncatedDump(err.v, 30)
}

type invalidPathIterError struct {
	v any
}

func (err *invalidPathIterError) Error() string {
	return "Invalid path expression near attempt to iterate through " + truncatedDump(err.v, 30)
}

type queryParseError struct {
	fname, contents string
	err             error
}

func (err *queryParseError) QueryParseError() (string, string, error) {
	return err.fname, err.contents, err.err
}

func (err *queryParseError) Error() string {
	return "invalid query: " + err.fname + ": " + err.err.Error()
}

type jsonParseError struct {
	fname, contents string
	err             error
}

func (err *jsonParseError) JSONParseError() (string, string, error) {
	return err.fname, err.contents, err.err
}

func (err *jsonParseError) Error() string {
	return "invalid json: " + err.fname + ": " + err.err.Error()
}

// typeErrorPreview is jq's "type (value)" error operand: the encoded value,
// cut to 11 bytes and an ellipsis past 14 bytes as jq's
// jv_dump_string_trunc does.
func typeErrorPreview(v any) string {
	if _, ok := v.(Iter); ok {
		return "gojq.Iter"
	}
	return TypeOf(v) + " (" + truncatedDump(v, 15) + ")"
}

// truncatedDump is jv_dump_string_trunc into a buffer of size bytes: a
// dump that does not fit keeps size-4 bytes and an ellipsis.
func truncatedDump(v any, size int) string {
	dump := jsonMarshal(v)
	if len(dump) > size-1 {
		dump = dump[:size-4] + "..."
	}
	return dump
}
