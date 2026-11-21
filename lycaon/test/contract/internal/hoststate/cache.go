// Package hoststate derives Cartesian prompt and OAR host-state fixtures,
// turn authorities, render cache keys, and branch coverage reports from live
// configuration catalogs and production resolvers.
package hoststate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

// ErrUncacheableRenderInput reports a variable outside the sound render-cache grammar.
var ErrUncacheableRenderInput = errors.New("hoststate: uncacheable render input")

var timeType = reflect.TypeFor[time.Time]()

type ptrVisit struct {
	typ reflect.Type
	ptr uintptr
}

// TryRenderCacheKey hashes templateID, catalogRevision, selectedUnits, and the
// complete fullVars map under the admitted value grammar: nil, bool, string,
// integers, finite floats, time.Time (instant + location + zone offset),
// slices/arrays, string-keyed maps, and structs whose fields are all exported.
// Any value outside that grammar returns ErrUncacheableRenderInput.
func TryRenderCacheKey(
	templateID, catalogRevision string,
	selectedUnits []string,
	fullVars map[string]any,
) (string, error) {
	encodedVars, err := encodeAdmittedValue(reflect.ValueOf(fullVars), map[ptrVisit]bool{})
	if err != nil {
		return "", err
	}
	units := slices.Clone(selectedUnits)
	if units == nil {
		units = []string{}
	}
	payload := map[string]any{
		"catalog_revision": catalogRevision,
		"selected_units":   units,
		"template_id":      templateID,
		"vars":             encodedVars,
	}
	return sha256CanonicalJSON(payload), nil
}

// RenderCacheKey returns the SHA-256 cache digest when fullVars fits the sound
// admitted grammar, or "" when fullVars contains an uncacheable value so
// RenderCache bypasses caching rather than risking collisions.
func RenderCacheKey(templateID, catalogRevision string, selectedUnits []string, fullVars map[string]any) string {
	key, err := TryRenderCacheKey(templateID, catalogRevision, selectedUnits, fullVars)
	if err != nil {
		return ""
	}
	return key
}

// AuthorityDigest hashes the offered tool set, projected argument schemas,
// native program reach vector, evidence mints, and subject reader reach.
func AuthorityDigest(
	offered []string,
	projectedSchemas map[string]map[string]any,
	programReach toolreach.Programs,
	mints map[string]bool,
	subjectReader prompts.Reader,
) string {
	sortedOffered := slices.Clone(offered)
	slices.Sort(sortedOffered)

	programs := make([]string, 0)
	if programReach != nil {
		if lang, err := toolcontract.ReadOnlyLanguageV1(); err == nil {
			for _, spec := range lang.Commands {
				if programReach(spec.Name) {
					programs = append(programs, spec.Name)
				}
			}
		}
		slices.Sort(programs)
	}

	encodedSchemas, err := encodeAdmittedValue(reflect.ValueOf(projectedSchemas), map[ptrVisit]bool{})
	if err != nil {
		return ""
	}
	encodedMints, err := encodeAdmittedValue(reflect.ValueOf(mints), map[ptrVisit]bool{})
	if err != nil {
		return ""
	}
	encodedHeld, err := encodeAdmittedValue(reflect.ValueOf(subjectReader.Held()), map[ptrVisit]bool{})
	if err != nil {
		return ""
	}

	payload := map[string]any{
		"mints":             encodedMints,
		"offered":           sortedOffered,
		"programs":          programs,
		"projected_schemas": encodedSchemas,
		"subject_held":      encodedHeld,
		"subject_profile":   subjectReader.ProfileID(),
		"subject_request":   subjectReader.Requestable(),
		"subject_surface":   subjectReader.SurfaceID(),
		"subject_valid":     subjectReader.Valid(),
	}
	return sha256CanonicalJSON(payload)
}

func sha256CanonicalJSON(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("hoststate: marshal canonical cache payload: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func encodeAdmittedValue(v reflect.Value, seen map[ptrVisit]bool) (any, error) {
	if !v.IsValid() {
		return []any{"nil", ""}, nil
	}
	var visits []ptrVisit
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			typeName := ""
			if v.Kind() == reflect.Pointer {
				typeName = v.Type().String()
			}
			return []any{"nil", typeName}, nil
		}
		if v.Kind() == reflect.Pointer {
			key := ptrVisit{typ: v.Type(), ptr: v.Pointer()}
			if seen[key] {
				return nil, fmt.Errorf("%w: cyclic pointer %s", ErrUncacheableRenderInput, v.Type())
			}
			seen[key] = true
			visits = append(visits, key)
		}
		v = v.Elem()
	}
	defer func() {
		for _, k := range visits {
			delete(seen, k)
		}
	}()

	typeName := v.Type().String()
	switch v.Kind() {
	case reflect.Bool:
		return []any{"bool", typeName, v.Bool()}, nil
	case reflect.String:
		return []any{"string", typeName, v.String()}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return []any{"int", typeName, strconv.FormatInt(v.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return []any{"uint", typeName, strconv.FormatUint(v.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%w: non-finite float %v", ErrUncacheableRenderInput, f)
		}
		return []any{"float", typeName, strconv.FormatUint(math.Float64bits(f), 16)}, nil
	case reflect.Slice, reflect.Array:
		return encodeAdmittedList(v, typeName, seen)
	case reflect.Map:
		return encodeAdmittedMap(v, typeName, seen)
	case reflect.Struct:
		return encodeAdmittedStruct(v, typeName, seen)
	default:
		return nil, fmt.Errorf("%w: unsupported kind %s (%s)", ErrUncacheableRenderInput, v.Kind(), typeName)
	}
}

func encodeAdmittedList(v reflect.Value, typeName string, seen map[ptrVisit]bool) (any, error) {
	isNil := v.Kind() == reflect.Slice && v.IsNil()
	items := make([]any, v.Len())
	for i := 0; i < v.Len(); i++ {
		enc, err := encodeAdmittedValue(v.Index(i), seen)
		if err != nil {
			return nil, err
		}
		items[i] = enc
	}
	return []any{"list", typeName, isNil, items}, nil
}

func encodeAdmittedMap(v reflect.Value, typeName string, seen map[ptrVisit]bool) (any, error) {
	if v.Type().Key().Kind() != reflect.String {
		return nil, fmt.Errorf("%w: non-string map key type %s", ErrUncacheableRenderInput, v.Type().Key())
	}
	out := make(map[string]any, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		enc, err := encodeAdmittedValue(iter.Value(), seen)
		if err != nil {
			return nil, err
		}
		out[iter.Key().String()] = enc
	}
	return []any{"map", typeName, v.IsNil(), out}, nil
}

func encodeAdmittedStruct(v reflect.Value, typeName string, seen map[ptrVisit]bool) (any, error) {
	if v.Type() == timeType {
		ts := v.Interface().(time.Time)
		zoneName, offsetSec := ts.Zone()
		locName := ""
		if loc := ts.Location(); loc != nil {
			locName = loc.String()
		}
		return []any{"time", ts.Unix(), ts.Nanosecond(), locName, zoneName, offsetSec}, nil
	}
	typ := v.Type()
	fields := make(map[string]any, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			return nil, fmt.Errorf("%w: struct %s has unexported field %s", ErrUncacheableRenderInput, typeName, field.Name)
		}
		enc, err := encodeAdmittedValue(v.Field(i), seen)
		if err != nil {
			return nil, err
		}
		fields[field.Name] = enc
	}
	return []any{"struct", typeName, fields}, nil
}

type authorityPair struct {
	renderKey       string
	authorityDigest string
}

type authorityFlight struct {
	done      chan struct{}
	err       error
	completed bool
}

// RenderCache deduplicates template renders by exact full-var-map digest and
// records authority verification only after a check completes without error.
type RenderCache struct {
	mu       sync.Mutex
	rendered map[string]string
	passed   map[authorityPair]bool
	inflight map[authorityPair]*authorityFlight
	hits     int
	misses   int
	bypasses int
}

// NewRenderCache constructs an empty render and authority-verification cache.
func NewRenderCache() *RenderCache {
	return &RenderCache{
		rendered: make(map[string]string),
		passed:   make(map[authorityPair]bool),
		inflight: make(map[authorityPair]*authorityFlight),
	}
}

// RenderOrReuse returns cached bytes on a valid RenderCacheKey hit, or executes
// render. Empty renderKey values bypass caching so uncacheable inputs never reuse bytes.
func (c *RenderCache) RenderOrReuse(renderKey string, render func() (string, error)) (string, bool, error) {
	if renderKey == "" {
		out, err := render()
		if err != nil {
			return "", false, err
		}
		c.mu.Lock()
		c.bypasses++
		c.mu.Unlock()
		return out, false, nil
	}

	c.mu.Lock()
	if out, ok := c.rendered[renderKey]; ok {
		c.hits++
		c.mu.Unlock()
		return out, true, nil
	}
	c.mu.Unlock()

	out, err := render()
	if err != nil {
		return "", false, err
	}

	c.mu.Lock()
	c.rendered[renderKey] = out
	c.misses++
	c.mu.Unlock()
	return out, false, nil
}

// CheckAuthority runs check for (renderKey, authorityDigest) unless a prior call
// completed with nil error. Failed or interrupted checks are never cached as
// passed, and overlapping concurrent calls share the in-flight outcome safely.
func (c *RenderCache) CheckAuthority(renderKey, authorityDigest string, check func() error) error {
	if renderKey == "" || authorityDigest == "" {
		return check()
	}
	pair := authorityPair{renderKey: renderKey, authorityDigest: authorityDigest}
	for {
		c.mu.Lock()
		if c.passed[pair] {
			c.mu.Unlock()
			return nil
		}
		if flight, busy := c.inflight[pair]; busy {
			c.mu.Unlock()
			<-flight.done
			if !flight.completed {
				// Leader panicked or called runtime.Goexit; retry rather than skipping.
				continue
			}
			return flight.err
		}
		flight := &authorityFlight{done: make(chan struct{})}
		c.inflight[pair] = flight
		c.mu.Unlock()
		return c.runAuthorityLeader(pair, flight, check)
	}
}

func (c *RenderCache) runAuthorityLeader(
	pair authorityPair,
	flight *authorityFlight,
	check func() error,
) error {
	var checkErr error
	finished := false
	defer func() {
		c.mu.Lock()
		delete(c.inflight, pair)
		if finished {
			flight.completed = true
			flight.err = checkErr
			if checkErr == nil {
				c.passed[pair] = true
			}
		}
		close(flight.done)
		c.mu.Unlock()
	}()

	checkErr = check()
	finished = true
	return checkErr
}

// AuthorityVerified reports whether (renderKey, authorityDigest) has completed
// a clean authority check.
func (c *RenderCache) AuthorityVerified(renderKey, authorityDigest string) bool {
	if renderKey == "" || authorityDigest == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.passed[authorityPair{renderKey: renderKey, authorityDigest: authorityDigest}]
}

// Stats returns exact render cache hits, misses, and uncacheable bypasses.
func (c *RenderCache) Stats() (hits, misses, bypasses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.bypasses
}
