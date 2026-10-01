// Package jsonshape decodes a JSON value into a Go type member by member,
// keeping every member that fits the type and naming each one that does not.
package jsonshape

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Kind is why a member was not decoded.
type Kind string

const (
	// Unknown is a member the type does not declare.
	Unknown Kind = "unknown"
	// Mismatch is a declared member whose value has the wrong JSON type.
	Mismatch Kind = "mismatch"
)

// Issue is one member left out of the decoded value.
type Issue struct {
	// Path locates the member: findings[3].ask.
	Path string
	// Pattern is Path with its array indexes dropped: findings[].ask.
	Pattern string
	// Parent is the Pattern of the object holding the member; empty at the root.
	Parent string
	// Name is the member's own key, or empty for an array element.
	Name string
	Kind Kind
	// Want is the JSON type the member needs, for a mismatch.
	Want string
}

var (
	// ErrNotJSON reports input that is not one JSON value.
	ErrNotJSON = errors.New("not a JSON value")
	// ErrWrongType reports a JSON value of the wrong type for the whole destination.
	ErrWrongType = errors.New("JSON value has the wrong type")
)

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// Decode fills dst, a non-nil pointer, from data. Members the type declares
// by their json tags are decoded; any other member, and any member of the
// wrong JSON type, is left out and reported. A mismatched array element is
// dropped from its array. The error is reserved for input that is not JSON,
// or whose top-level value cannot fill dst at all.
func Decode(data []byte, dst any) ([]Issue, error) {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, fmt.Errorf("jsonshape: destination must be a non-nil pointer, got %T", dst)
	}
	if !json.Valid(data) {
		return nil, ErrNotJSON
	}
	var d decoder
	if !d.value(json.RawMessage(data), v.Elem(), location{}) {
		return nil, fmt.Errorf("%w: want %s", ErrWrongType, want(v.Elem().Type()))
	}
	return d.issues, nil
}

// Fields lists the json member names a struct type declares, in field order.
func Fields(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for _, f := range structFields(t) {
		out = append(out, f.name)
	}
	return out
}

type decoder struct {
	issues []Issue
}

// location tracks where a member sits, with and without array indexes.
type location struct {
	path, pattern string
}

func (l location) member(name string) location {
	if l.path == "" {
		return location{path: name, pattern: name}
	}
	return location{path: l.path + "." + name, pattern: l.pattern + "." + name}
}

func (l location) element(i int) location {
	return location{path: l.path + "[" + strconv.Itoa(i) + "]", pattern: l.pattern + "[]"}
}

func (d *decoder) report(at location, parent location, name string, kind Kind, want string) {
	d.issues = append(d.issues, Issue{
		Path: at.path, Pattern: at.pattern, Parent: parent.pattern, Name: name, Kind: kind, Want: want,
	})
}

// value decodes raw into v and reports whether v now holds it. A mismatch at
// v itself is reported against the member that holds it.
func (d *decoder) value(raw json.RawMessage, v reflect.Value, at location) bool {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if string(raw) == "null" {
		return true
	}
	t := v.Type()
	if t.Kind() == reflect.Pointer {
		elem := reflect.New(t.Elem())
		if !d.value(raw, elem.Elem(), at) {
			return false
		}
		v.Set(elem)
		return true
	}
	if reflect.PointerTo(t).Implements(unmarshalerType) {
		return json.Unmarshal(raw, v.Addr().Interface()) == nil
	}
	switch t.Kind() {
	case reflect.Struct:
		return d.object(raw, v, at)
	case reflect.Slice:
		return d.array(raw, v, at)
	case reflect.Map:
		return d.dict(raw, v, at)
	default:
		return json.Unmarshal(raw, v.Addr().Interface()) == nil
	}
}

func (d *decoder) object(raw json.RawMessage, v reflect.Value, at location) bool {
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return false
	}
	fields := map[string]field{}
	for _, f := range structFields(v.Type()) {
		fields[f.name] = f
	}
	for _, name := range sortedKeys(members) {
		f, ok := fields[name]
		child := at.member(name)
		if !ok {
			d.report(child, at, name, Unknown, "")
			continue
		}
		target := v.FieldByIndex(f.index)
		if !d.value(members[name], target, child) {
			target.SetZero()
			d.report(child, at, name, Mismatch, want(target.Type()))
		}
	}
	return true
}

func (d *decoder) array(raw json.RawMessage, v reflect.Value, at location) bool {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false
	}
	out := reflect.MakeSlice(v.Type(), 0, len(items))
	for i, item := range items {
		elem := reflect.New(v.Type().Elem()).Elem()
		if !d.value(item, elem, at.element(i)) {
			d.report(at.element(i), at, "", Mismatch, want(elem.Type()))
			continue
		}
		out = reflect.Append(out, elem)
	}
	v.Set(out)
	return true
}

func (d *decoder) dict(raw json.RawMessage, v reflect.Value, at location) bool {
	if v.Type().Key().Kind() != reflect.String {
		return json.Unmarshal(raw, v.Addr().Interface()) == nil
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return false
	}
	out := reflect.MakeMapWithSize(v.Type(), len(members))
	for _, name := range sortedKeys(members) {
		elem := reflect.New(v.Type().Elem()).Elem()
		child := at.member(name)
		if !d.value(members[name], elem, child) {
			d.report(child, at, name, Mismatch, want(elem.Type()))
			continue
		}
		out.SetMapIndex(reflect.ValueOf(name).Convert(v.Type().Key()), elem)
	}
	v.Set(out)
	return true
}

type field struct {
	name  string
	index []int
}

// structFields are the members encoding/json would decode, embedded structs
// flattened.
func structFields(t reflect.Type) []field {
	var out []field
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || f.Anonymous && f.Type.Kind() == reflect.Struct {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, field{name: name, index: f.Index})
	}
	return out
}

// want names the JSON type a Go type decodes from.
func want(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return "value"
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
