// Package hoststate derives Cartesian prompt and OAR host-state fixtures,
// turn authorities, render cache keys, and branch coverage reports from live
// configuration catalogs and production resolvers.
package hoststate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"reflect"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

// ErrUncacheableRenderInput reports a variable outside the sound render-cache grammar.
var ErrUncacheableRenderInput = errors.New("hoststate: uncacheable render input")

type ptrVisit struct {
	kind   reflect.Kind
	typ    reflect.Type
	ptr    uintptr
	length int
}

// TryRenderCacheKey hashes templateID, catalogRevision, selectedUnits, and the
// complete fullVars map. Unsupported render inputs return ErrUncacheableRenderInput.
func TryRenderCacheKey(
	templateID, catalogRevision string,
	selectedUnits []string,
	fullVars map[string]any,
) (string, error) {
	h := sha256.New()
	writeHashString(h, "render-v2")
	writeHashString(h, catalogRevision)
	writeHashString(h, templateID)
	writeHashStringSlice(h, selectedUnits)
	if err := hashAdmittedValue(h, reflect.ValueOf(fullVars), map[ptrVisit]bool{}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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

func computeProgramsList(programReach toolreach.Programs) []string {
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
	return programs
}

// AuthorityDigest hashes the offered tool set, projected argument schemas,
// native program reach vector, evidence mints, and subject reader reach.
// Uncacheable projected schema values return "" so CheckAuthority bypasses reuse.
func AuthorityDigest(
	offered []string,
	projectedSchemas map[string]map[string]any,
	programReach toolreach.Programs,
	mints map[string]bool,
	subjectReader prompts.Reader,
) string {
	sortedOffered := slices.Clone(offered)
	slices.Sort(sortedOffered)
	programs := computeProgramsList(programReach)
	schemasDigest, err := digestAdmittedValue(projectedSchemas)
	if err != nil {
		return ""
	}
	return authorityDigestPrecomputed(sortedOffered, schemasDigest, programs, mints, subjectReader)
}

func authorityDigestPrecomputed(
	sortedOffered []string,
	schemasDigest string,
	programs []string,
	mints map[string]bool,
	subjectReader prompts.Reader,
) string {
	h := sha256.New()
	writeHashString(h, "auth-v2")
	writeHashBoolMap(h, mints)
	writeHashStringSlice(h, sortedOffered)
	writeHashStringSlice(h, programs)
	writeHashString(h, schemasDigest)
	writeHashBoolMap(h, subjectReader.Held())
	writeHashString(h, subjectReader.ProfileID())
	writeHashStringSlice(h, subjectReader.Requestable())
	writeHashString(h, subjectReader.SurfaceID())
	writeHashBool(h, subjectReader.Valid())
	return hex.EncodeToString(h.Sum(nil))
}

func digestAdmittedValue(v any) (string, error) {
	h := sha256.New()
	if err := hashAdmittedValue(h, reflect.ValueOf(v), map[ptrVisit]bool{}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeHashUint64(h hash.Hash, n uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	_, _ = h.Write(buf[:])
}

func writeHashBool(h hash.Hash, b bool) {
	if b {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
}

func writeHashString(h hash.Hash, s string) {
	writeHashUint64(h, uint64(len(s)))
	_, _ = h.Write([]byte(s))
}

func writeHashStringSlice(h hash.Hash, items []string) {
	writeHashUint64(h, uint64(len(items)))
	for _, s := range items {
		writeHashString(h, s)
	}
}

func writeHashBoolMap(h hash.Hash, m map[string]bool) {
	writeHashBool(h, m == nil)
	writeHashUint64(h, uint64(len(m)))
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		writeHashString(h, k)
		writeHashBool(h, m[k])
	}
}

func hasAnyMethods(t reflect.Type) bool {
	if t.NumMethod() > 0 {
		return true
	}
	if t.Kind() != reflect.Pointer && t.Kind() != reflect.Interface {
		return reflect.PointerTo(t).NumMethod() > 0
	}
	return false
}

func hashAdmittedValue(h hash.Hash, v reflect.Value, seen map[ptrVisit]bool) error {
	if !v.IsValid() {
		_, _ = h.Write([]byte{'N'})
		return nil
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			_, _ = h.Write([]byte{'N'})
			return nil
		}
		v = v.Elem()
	}

	typ := v.Type()
	if hasAnyMethods(typ) {
		return fmt.Errorf("%w: type %s defines methods", ErrUncacheableRenderInput, typ)
	}
	typeName := typ.String()

	switch v.Kind() {
	case reflect.Pointer:
		_, _ = h.Write([]byte{'P'})
		writeHashString(h, typeName)
		if v.IsNil() {
			_, _ = h.Write([]byte{0})
			return nil
		}
		_, _ = h.Write([]byte{1})
		key := ptrVisit{kind: reflect.Pointer, typ: typ, ptr: v.Pointer()}
		if seen[key] {
			return fmt.Errorf("%w: cyclic pointer %s", ErrUncacheableRenderInput, typeName)
		}
		seen[key] = true
		defer delete(seen, key)
		return hashAdmittedValue(h, v.Elem(), seen)
	case reflect.Bool:
		_, _ = h.Write([]byte{'B'})
		writeHashString(h, typeName)
		writeHashBool(h, v.Bool())
		return nil
	case reflect.String:
		_, _ = h.Write([]byte{'S'})
		writeHashString(h, typeName)
		writeHashString(h, v.String())
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		_, _ = h.Write([]byte{'I'})
		writeHashString(h, typeName)
		writeHashUint64(h, uint64(v.Int()))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		_, _ = h.Write([]byte{'U'})
		writeHashString(h, typeName)
		writeHashUint64(h, v.Uint())
		return nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("%w: non-finite float %v", ErrUncacheableRenderInput, f)
		}
		_, _ = h.Write([]byte{'F'})
		writeHashString(h, typeName)
		writeHashUint64(h, math.Float64bits(f))
		return nil
	case reflect.Slice, reflect.Array:
		return hashAdmittedList(h, v, typeName, seen)
	case reflect.Map:
		return hashAdmittedMap(h, v, typeName, seen)
	case reflect.Struct:
		return hashAdmittedStruct(h, v, typeName, seen)
	default:
		return fmt.Errorf("%w: unsupported kind %s (%s)", ErrUncacheableRenderInput, v.Kind(), typeName)
	}
}

func hashAdmittedList(h hash.Hash, v reflect.Value, typeName string, seen map[ptrVisit]bool) error {
	isNil := v.Kind() == reflect.Slice && v.IsNil()
	if v.Kind() == reflect.Slice && !isNil && v.Len() > 0 {
		key := ptrVisit{kind: reflect.Slice, typ: v.Type(), ptr: v.Pointer(), length: v.Len()}
		if seen[key] {
			return fmt.Errorf("%w: cyclic slice %s", ErrUncacheableRenderInput, typeName)
		}
		seen[key] = true
		defer delete(seen, key)
	}
	_, _ = h.Write([]byte{'L'})
	writeHashString(h, typeName)
	writeHashBool(h, isNil)
	n := v.Len()
	writeHashUint64(h, uint64(n))
	for i := 0; i < n; i++ {
		if err := hashAdmittedValue(h, v.Index(i), seen); err != nil {
			return err
		}
	}
	return nil
}

func hashAdmittedMap(h hash.Hash, v reflect.Value, typeName string, seen map[ptrVisit]bool) error {
	if v.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("%w: non-string map key type %s", ErrUncacheableRenderInput, v.Type().Key())
	}
	isNil := v.IsNil()
	if !isNil {
		key := ptrVisit{kind: reflect.Map, typ: v.Type(), ptr: v.Pointer()}
		if seen[key] {
			return fmt.Errorf("%w: cyclic map %s", ErrUncacheableRenderInput, typeName)
		}
		seen[key] = true
		defer delete(seen, key)
	}
	_, _ = h.Write([]byte{'M'})
	writeHashString(h, typeName)
	writeHashBool(h, isNil)
	n := v.Len()
	writeHashUint64(h, uint64(n))
	if n == 0 {
		return nil
	}
	type mapKV struct {
		k string
		v reflect.Value
	}
	entries := make([]mapKV, 0, n)
	iter := v.MapRange()
	for iter.Next() {
		entries = append(entries, mapKV{k: iter.Key().String(), v: iter.Value()})
	}
	slices.SortFunc(entries, func(a, b mapKV) int {
		if a.k < b.k {
			return -1
		}
		if a.k > b.k {
			return 1
		}
		return 0
	})
	for _, e := range entries {
		writeHashString(h, e.k)
		if err := hashAdmittedValue(h, e.v, seen); err != nil {
			return err
		}
	}
	return nil
}

func hashAdmittedStruct(h hash.Hash, v reflect.Value, typeName string, seen map[ptrVisit]bool) error {
	typ := v.Type()
	_, _ = h.Write([]byte{'T'})
	writeHashString(h, typeName)
	n := v.NumField()
	writeHashUint64(h, uint64(n))
	for i := 0; i < n; i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			return fmt.Errorf("%w: struct %s has unexported field %s", ErrUncacheableRenderInput, typeName, field.Name)
		}
		writeHashString(h, field.Name)
		if err := hashAdmittedValue(h, v.Field(i), seen); err != nil {
			return err
		}
	}
	return nil
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
