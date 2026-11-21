// Package hoststate derives Cartesian prompt and OAR host-state fixtures,
// turn authorities, render cache keys, and branch coverage reports from live
// configuration catalogs and production resolvers.
package hoststate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

// RenderCacheKey hashes the template identifier, catalog revision, selected
// prompt units, and canonical sorted JSON of the complete variable map.
func RenderCacheKey(templateID, catalogRevision string, selectedUnits []string, fullVars map[string]any) string {
	units := slices.Clone(selectedUnits)
	if units == nil {
		units = []string{}
	}
	payload := map[string]any{
		"catalog_revision": catalogRevision,
		"selected_units":   units,
		"template_id":      templateID,
		"vars":             canonicalValue(reflect.ValueOf(fullVars)),
	}
	return sha256CanonicalJSON(payload)
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

	payload := map[string]any{
		"mints":             canonicalValue(reflect.ValueOf(mints)),
		"offered":           sortedOffered,
		"programs":          programs,
		"projected_schemas": canonicalValue(reflect.ValueOf(projectedSchemas)),
		"subject_held":      canonicalValue(reflect.ValueOf(subjectReader.Held())),
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

func canonicalValue(v reflect.Value) any {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = canonicalValue(v.Index(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key().Interface())
			out[key] = canonicalValue(iter.Value())
		}
		return out
	case reflect.Struct:
		return canonicalStruct(v)
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

func canonicalStruct(v reflect.Value) map[string]any {
	typ := v.Type()
	out := make(map[string]any, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		out[field.Name] = canonicalValue(v.Field(i))
	}
	return out
}

// RenderCache deduplicates template renders by exact full-var-map digest while
// tracking which turn authorities have verified each cached byte payload.
type RenderCache struct {
	mu       sync.Mutex
	rendered map[string]string
	verified map[string]map[string]bool
	hits     int
	misses   int
}

// NewRenderCache constructs an empty render and authority-verification cache.
func NewRenderCache() *RenderCache {
	return &RenderCache{
		rendered: make(map[string]string),
		verified: make(map[string]map[string]bool),
	}
}

// RenderOrReuse returns cached bytes on exact RenderCacheKey hit or executes render.
func (c *RenderCache) RenderOrReuse(renderKey string, render func() (string, error)) (string, bool, error) {
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

// NeedsAuthorityCheck reports whether authorityDigest has not yet been verified
// against renderKey, and records it as checked when atomically claimed.
func (c *RenderCache) NeedsAuthorityCheck(renderKey, authorityDigest string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	byAuth := c.verified[renderKey]
	if byAuth == nil {
		byAuth = make(map[string]bool)
		c.verified[renderKey] = byAuth
	}
	if byAuth[authorityDigest] {
		return false
	}
	byAuth[authorityDigest] = true
	return true
}

// Stats returns exact render cache hits and misses.
func (c *RenderCache) Stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}
