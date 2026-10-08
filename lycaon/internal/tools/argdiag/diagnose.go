package argdiag

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonvalue"
)

// maxReplacementArgsBytes bounds a repaired call echoed back in a refusal.
// Above it the refusal names the paths to fix instead of restating the call.
const maxReplacementArgsBytes = 3072

// Diagnosis explains a schema failure in terms of what the caller wrote.
type Diagnosis struct {
	// JSONText is the first structured slot that received JSON as a string.
	JSONText *JSONText
	// Misplaced groups members written under one object that the schema
	// declares under another, outside any JSON text.
	Misplaced    []Misplacement
	DidYouMean   *NearMiss
	ConflictKeys []string
	// Replacement is the call with every repairable finding applied; it is
	// set only when it validates and fits maxReplacementArgsBytes.
	Replacement map[string]any
}

// JSONText is a declared object or array slot that received a string.
type JSONText struct {
	Path         string
	ExpectedType string
	// Defect is nil when the text is valid JSON.
	Defect *Defect
	// Misplaced is what the text's own structure misplaces as written.
	Misplaced []Misplacement
	// TrailingMembers were written after the value closed; the schema declares
	// them under TrailingBelongsUnder, the slot itself or the object beside it.
	TrailingMembers      []string
	TrailingBelongsUnder string
}

// Misplacement is a set of members found under one object and declared under
// another, adjacent one.
type Misplacement struct {
	Fields       []string
	FoundUnder   string
	BelongsUnder string
	// CloseBefore names the first misplaced member in document order, where the
	// object it was read into should have closed. Known only for JSON text.
	CloseBefore string
}

type NearMiss struct {
	Path       string
	DidYouMean string
}

// Diagnose walks args against schema at every depth; validate decides whether
// the repaired call satisfies the schema.
func Diagnose(schema, args map[string]any, validate func(schema, args map[string]any) error) Diagnosis {
	var d Diagnosis
	if schema == nil || args == nil {
		return d
	}
	repaired := jsonvalue.CloneMap(args)
	w := &argsWalker{diag: &d}
	w.object(objectNode{schema: schema, value: repaired})
	d.Misplaced = w.top.groups()
	d.ConflictKeys = detectCombinatorConflicts(schema, args)
	if len(w.repairs) == 0 {
		return d
	}
	for _, repair := range w.repairs {
		repair()
	}
	if validate(schema, repaired) != nil {
		return d
	}
	if b, err := json.MarshalIndent(repaired, "", "  "); err == nil && len(b) <= maxReplacementArgsBytes {
		d.Replacement = repaired
	}
	return d
}

// objectNode is one argument object paired with the schema it must satisfy.
type objectNode struct {
	path   string
	schema map[string]any
	value  map[string]any
	// parent is the enclosing object; nil at the root and inside arrays,
	// since a member cannot move out of an array element.
	parent *objectNode
	// order is the members' document order when known.
	order []string
}

type argsWalker struct {
	diag    *Diagnosis
	top     misplacementSet
	repairs []func()
}

// misplacementSet groups moves by source and destination object.
type misplacementSet struct {
	byPair map[[2]string]*Misplacement
	pairs  [][2]string
}

func (s *misplacementSet) add(found, belongs, field string) *Misplacement {
	if s.byPair == nil {
		s.byPair = map[[2]string]*Misplacement{}
	}
	key := [2]string{found, belongs}
	m, ok := s.byPair[key]
	if !ok {
		m = &Misplacement{FoundUnder: found, BelongsUnder: belongs}
		s.byPair[key] = m
		s.pairs = append(s.pairs, key)
	}
	m.Fields = append(m.Fields, field)
	return m
}

func (s *misplacementSet) groups() []Misplacement {
	out := make([]Misplacement, 0, len(s.pairs))
	for _, key := range s.pairs {
		out = append(out, *s.byPair[key])
	}
	return out
}

func (w *argsWalker) object(n objectNode) { w.objectInto(n, &w.top) }

func (w *argsWalker) objectInto(n objectNode, sink *misplacementSet) {
	declared := declaredProperties(n.schema)
	keys := memberOrder(n)
	var unknown []string
	for _, key := range keys {
		propSchema, ok := declared[key]
		if !ok {
			if closedObject(n.schema) {
				unknown = append(unknown, key)
			}
			continue
		}
		w.member(n, key, propSchema, sink)
	}
	var unplaced []string
	for _, key := range unknown {
		if !w.relocate(n, key, declared, sink) {
			unplaced = append(unplaced, key)
		}
	}
	if w.diag.DidYouMean != nil || len(unplaced) == 0 {
		return
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	if match, field := findClosestPropertyMatch(unplaced, names); match != "" {
		w.diag.DidYouMean = &NearMiss{Path: joinArgPath(n.path, field), DidYouMean: match}
	}
}

// member checks one declared member and descends into its structure.
func (w *argsWalker) member(n objectNode, key string, propSchema map[string]any, sink *misplacementSet) {
	path := joinArgPath(n.path, key)
	switch value := n.value[key].(type) {
	case string:
		if parsed, ok := w.jsonText(path, propSchema, value, &n); ok {
			container := n.value
			w.repairs = append(w.repairs, func() { container[key] = parsed })
		}
	case map[string]any:
		w.objectInto(objectNode{path: path, schema: propSchema, value: value, parent: &n}, sink)
	case []any:
		w.elements(path, propSchema, value, sink)
	}
}

func (w *argsWalker) elements(path string, arraySchema map[string]any, values []any, sink *misplacementSet) {
	items, _ := arraySchema["items"].(map[string]any)
	if items == nil {
		return
	}
	for i, item := range values {
		itemPath := path + "[" + strconv.Itoa(i) + "]"
		switch value := item.(type) {
		case map[string]any:
			w.objectInto(objectNode{path: itemPath, schema: items, value: value}, sink)
		case string:
			if parsed, ok := w.jsonText(itemPath, items, value, nil); ok {
				index := i
				w.repairs = append(w.repairs, func() { values[index] = parsed })
			}
		}
	}
}

// jsonText reads a string in a structured slot. It returns the value the
// slot should hold: the decoded text, or the text's structure as written with
// its misplaced members moved.
func (w *argsWalker) jsonText(path string, slot map[string]any, text string, parent *objectNode) (any, bool) {
	expects, expectedType := schemaExpectsStructuredJSON(slot)
	trimmed := strings.TrimSpace(text)
	if !expects || trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, false
	}
	finding := &JSONText{Path: path, ExpectedType: expectedType}
	var value any
	var order map[string][]string
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		read := readLenientJSON(trimmed)
		defect := read.Defect
		for i, open := range defect.OpenPaths {
			defect.OpenPaths[i] = joinRelative(path, open)
		}
		finding.Defect = &defect
		value, order = read.Value, read.KeyOrder
		if object, ok := value.(map[string]any); ok {
			w.placeTrailingMembers(finding, object, slot, parent, read)
		}
	}
	var inner misplacementSet
	switch v := value.(type) {
	case map[string]any:
		w.objectInto(objectNode{path: path, schema: slot, value: v, order: order[""]}, &inner)
		if order != nil {
			inner.closeBefore(path, order)
		}
	case []any:
		w.elements(path, slot, v, &inner)
	}
	finding.Misplaced = inner.groups()
	if w.diag.JSONText == nil {
		w.diag.JSONText = finding
	}
	return value, value != nil
}

// placeTrailingMembers places members written after the value closed: into
// the value when its slot declares them, or beside it when the enclosing
// object does. The first destination is the one the refusal names.
func (w *argsWalker) placeTrailingMembers(finding *JSONText, object, slot map[string]any, parent *objectNode, read lenientJSON) {
	inside := declaredProperties(slot)
	var beside map[string]map[string]any
	if parent != nil {
		beside = declaredProperties(parent.schema)
	}
	for _, key := range read.TrailingOrder {
		value := read.Trailing[key]
		var dest string
		switch {
		case inside[key] != nil && object[key] == nil:
			object[key] = value
			dest = finding.Path
		case beside[key] != nil && parent.value[key] == nil:
			container, member := parent.value, key
			w.repairs = append(w.repairs, func() { container[member] = value })
			dest = parent.path
		default:
			continue
		}
		if len(finding.TrailingMembers) == 0 {
			finding.TrailingBelongsUnder = dest
		}
		if dest == finding.TrailingBelongsUnder {
			finding.TrailingMembers = append(finding.TrailingMembers, key)
		}
	}
}

// closeBefore names, for each upward move read from JSON text, the first
// misplaced member in the order the text wrote the object it landed in.
func (s *misplacementSet) closeBefore(root string, order map[string][]string) {
	for _, key := range s.pairs {
		m := s.byPair[key]
		if !strings.HasPrefix(m.FoundUnder, m.BelongsUnder) {
			continue
		}
		for _, member := range order[relativeArgPath(root, m.FoundUnder)] {
			if slices.Contains(m.Fields, member) {
				m.CloseBefore = member
				break
			}
		}
	}
}

// relocate moves an undeclared member to the one adjacent object that
// declares it and lacks it: the parent, or one child object.
func (w *argsWalker) relocate(n objectNode, key string, declared map[string]map[string]any, sink *misplacementSet) bool {
	type target struct {
		path   string
		schema map[string]any
		apply  func(value any)
	}
	var candidates []target
	if p := n.parent; p != nil {
		if schema, ok := declaredProperties(p.schema)[key]; ok {
			if _, present := p.value[key]; !present {
				parent := p.value
				candidates = append(candidates, target{p.path, schema, func(v any) { parent[key] = v }})
			}
		}
	}
	children := make([]string, 0, len(declared))
	for name := range declared {
		children = append(children, name)
	}
	sort.Strings(children)
	for _, child := range children {
		childSchema := declared[child]
		schema, ok := declaredProperties(childSchema)[key]
		if !ok {
			continue
		}
		existing, present := n.value[child]
		childMap, isMap := existing.(map[string]any)
		if present && (!isMap || childMap[key] != nil) {
			continue
		}
		node, name := n.value, child
		candidates = append(candidates, target{joinArgPath(n.path, child), schema, func(v any) {
			m, ok := node[name].(map[string]any)
			if !ok {
				m = map[string]any{}
				node[name] = m
			}
			m[key] = v
		}})
	}
	if len(candidates) != 1 {
		return false
	}
	dest := candidates[0]
	sink.add(n.path, dest.path, key)
	value := n.value[key]
	if text, ok := value.(string); ok {
		if parsed, ok := w.jsonText(joinArgPath(n.path, key), dest.schema, text, nil); ok {
			value = parsed
		}
	}
	source := n.value
	w.repairs = append(w.repairs, func() {
		delete(source, key)
		dest.apply(value)
	})
	return true
}

func memberOrder(n objectNode) []string {
	if len(n.order) > 0 {
		return n.order
	}
	keys := make([]string, 0, len(n.value))
	for key := range n.value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func closedObject(schema map[string]any) bool {
	v, ok := schema["additionalProperties"].(bool)
	return ok && !v
}

// joinRelative prefixes a path relative to a JSON text with the slot's path.
func joinRelative(slot, rel string) string {
	switch {
	case rel == "":
		return slot
	case strings.HasPrefix(rel, "["):
		return slot + rel
	default:
		return joinArgPath(slot, rel)
	}
}

// relativeArgPath strips the slot prefix joinRelative added.
func relativeArgPath(slot, path string) string {
	if path == slot {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(path, slot), ".")
}

// AddFacts publishes the primary finding and the repair for refusal copy.
func (d Diagnosis) AddFacts(data map[string]any) {
	switch {
	case d.JSONText != nil:
		data["field"] = d.JSONText.Path
		data["expected_type"] = d.JSONText.ExpectedType
		if defect := d.JSONText.Defect; defect != nil {
			data["json_malformed"] = true
			defect.addFacts(data)
			if len(d.JSONText.TrailingMembers) > 0 {
				data["json_trailing_members"] = d.JSONText.TrailingMembers
				data["json_trailing_belongs_under"] = d.JSONText.TrailingBelongsUnder
			}
		} else {
			data["json_encoded"] = true
		}
		switch {
		case len(d.JSONText.Misplaced) > 0:
			d.JSONText.Misplaced[0].addFacts(data)
		case len(d.Misplaced) > 0:
			d.Misplaced[0].addFacts(data)
		}
	case len(d.Misplaced) > 0:
		m := d.Misplaced[0]
		data["field"] = joinArgPath(m.FoundUnder, m.Fields[0])
		m.addFacts(data)
	case d.DidYouMean != nil:
		data["field"] = d.DidYouMean.Path
	}
	if d.DidYouMean != nil {
		data["did_you_mean"] = d.DidYouMean.DidYouMean
	}
	if len(d.ConflictKeys) > 0 {
		data["conflict_keys"] = d.ConflictKeys
	}
	if d.Replacement != nil {
		data["replacement_args"] = d.Replacement
		if b, err := json.MarshalIndent(d.Replacement, "", "  "); err == nil {
			data["replacement_args_json"] = string(b)
		}
	}
}

// addFacts names the defect by kind; a position is the 1-based byte of the
// text where reading stopped.
func (d Defect) addFacts(data map[string]any) {
	at := d.Offset + 1
	switch d.Kind {
	case DefectUnclosed:
		data["json_open_paths"] = d.OpenPaths
	case DefectUnterminatedString:
		data["json_unterminated_string_at"] = at
	case DefectTrailingText:
		data["json_trailing_text_at"] = at
	default:
		data["json_unexpected_token_at"] = at
	}
}

func (m Misplacement) addFacts(data map[string]any) {
	data["misplaced_fields"] = append([]string(nil), m.Fields...)
	data["found_under"] = m.FoundUnder
	data["belongs_under"] = m.BelongsUnder
	if m.CloseBefore != "" {
		data["close_before"] = m.CloseBefore
	}
}
