// Package oardraft reads rule documents written for the OAR 1.0 candidate,
// the draft Painted Wolf 1.0.2 shipped, and rewrites them in the published
// OAR 1.0 shape. A candidate document names its version with "oar"; a 1.0
// document names it with "spec_version".
package oardraft

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Unconvertible is a candidate document with no OAR 1.0 equivalent.
type Unconvertible struct {
	Field  string
	Reason string
}

func (e *Unconvertible) Error() string {
	return fmt.Sprintf("OAR 1.0 candidate rule cannot be read as OAR 1.0: %s %s", e.Field, e.Reason)
}

// copyFields are the candidate's presentation strings, top-level in OAR 1.0.
var copyFields = map[string]bool{"title": true, "what": true, "cause": true, "why": true, "fix": true, "instead": true}

// dropped fields never affect a decision, and OAR 1.0 has no place for them.
var dropped = map[string]bool{"status": true, "related": true}

// Upgrade returns data unchanged unless it is a candidate document, which it
// rewrites in the OAR 1.0 shape. A candidate document that relies on
// something OAR 1.0 removed is refused with an *Unconvertible error.
func Upgrade(data []byte) ([]byte, bool, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return data, false, nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode || index(doc, "oar") < 0 || index(doc, "spec_version") >= 0 {
		return data, false, nil
	}
	if err := upgrade(doc); err != nil {
		return nil, false, err
	}
	out, err := encode(&root, json.Valid(data))
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func upgrade(doc *yaml.Node) error {
	version := value(doc, "oar")
	if version.Kind != yaml.ScalarNode || version.Value != "1.0" {
		return &Unconvertible{Field: "oar", Reason: fmt.Sprintf("names version %q; only the 1.0 candidate is read", version.Value)}
	}
	if effect := value(doc, "effect"); effect != nil && effect.Value == "transform" {
		return &Unconvertible{Field: "effect", Reason: "transform was removed from OAR 1.0"}
	}
	if index(doc, "transform") >= 0 {
		return &Unconvertible{Field: "transform", Reason: "was removed from OAR 1.0"}
	}
	if enforcement := value(doc, "enforcement"); enforcement != nil && enforcement.Value == "off" {
		return &Unconvertible{Field: "enforcement", Reason: "off was removed from OAR 1.0; disable the rule in configuration"}
	}
	if overrides := value(doc, "overrides"); overrides != nil && len(overrides.Content) > 0 {
		return &Unconvertible{Field: "overrides", Reason: "was removed from OAR 1.0; express the exclusion in the rule's condition"}
	}

	rename(doc, "oar", "spec_version")
	value(doc, "spec_version").Style = yaml.DoubleQuotedStyle
	rename(doc, "anchor", "on")
	if err := upgradeSelector(doc); err != nil {
		return err
	}
	if err := upgradeRequires(doc); err != nil {
		return err
	}
	if err := hoistCopy(doc); err != nil {
		return err
	}
	if index(doc, "message") < 0 {
		rename(doc, "x-paintedwolf-message", "message")
	}
	rename(doc, "counter_scope", "x-paintedwolf-counter-scope")
	for key := range dropped {
		remove(doc, key)
	}
	remove(doc, "overrides")
	return nil
}

// upgradeSelector keeps the tool clause, the only one OAR 1.0 selects on, and
// gives every rule the selector OAR 1.0 requires.
func upgradeSelector(doc *yaml.Node) error {
	selector := value(doc, "selector")
	if selector == nil {
		insertAfter(doc, "on", "selector", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle})
		return nil
	}
	if selector.Kind != yaml.MappingNode {
		return &Unconvertible{Field: "selector", Reason: "is not a mapping"}
	}
	for i := 0; i < len(selector.Content); i += 2 {
		if key := selector.Content[i].Value; key != "tool" {
			return &Unconvertible{Field: "selector." + key, Reason: "has no OAR 1.0 selector; move it into the rule's condition"}
		}
		selector.Content[i].Value = "tools"
	}
	return nil
}

// upgradeRequires keeps the facts a rule needs. Capability profile names have
// no OAR 1.0 field: a 1.0 rule's portability follows from its facts.
func upgradeRequires(doc *yaml.Node) error {
	requires := value(doc, "requires")
	if requires == nil {
		return nil
	}
	if requires.Kind != yaml.MappingNode {
		return &Unconvertible{Field: "requires", Reason: "is not a mapping"}
	}
	var facts *yaml.Node
	for i := 0; i < len(requires.Content); i += 2 {
		switch key := requires.Content[i].Value; key {
		case "facts":
			facts = requires.Content[i+1]
		case "profiles":
		default:
			return &Unconvertible{Field: "requires." + key, Reason: "has no OAR 1.0 field"}
		}
	}
	at := index(doc, "requires")
	if facts == nil {
		remove(doc, "requires")
		return nil
	}
	facts.Style = yaml.FlowStyle
	doc.Content[at].Value = "requires_facts"
	doc.Content[at+1] = facts
	return nil
}

// hoistCopy moves each presentation string to the top level, where OAR 1.0
// keeps it.
func hoistCopy(doc *yaml.Node) error {
	at := index(doc, "copy")
	if at < 0 {
		return nil
	}
	block := doc.Content[at+1]
	if block.Kind != yaml.MappingNode {
		return &Unconvertible{Field: "copy", Reason: "is not a mapping"}
	}
	for i := 0; i < len(block.Content); i += 2 {
		key := block.Content[i].Value
		if !copyFields[key] {
			return &Unconvertible{Field: "copy." + key, Reason: "has no OAR 1.0 field"}
		}
		if index(doc, key) >= 0 {
			return &Unconvertible{Field: "copy." + key, Reason: "is also set at the top level"}
		}
	}
	hoisted := append([]*yaml.Node(nil), doc.Content[:at]...)
	hoisted = append(hoisted, block.Content...)
	doc.Content = append(hoisted, doc.Content[at+2:]...)
	return nil
}

func index(doc *yaml.Node, key string) int {
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func value(doc *yaml.Node, key string) *yaml.Node {
	if i := index(doc, key); i >= 0 {
		return doc.Content[i+1]
	}
	return nil
}

func rename(doc *yaml.Node, from, to string) {
	if i := index(doc, from); i >= 0 {
		doc.Content[i].Value = to
	}
}

func remove(doc *yaml.Node, key string) {
	if i := index(doc, key); i >= 0 {
		doc.Content = append(doc.Content[:i], doc.Content[i+2:]...)
	}
}

func insertAfter(doc *yaml.Node, after, key string, val *yaml.Node) {
	pair := []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val}
	at := len(doc.Content)
	if i := index(doc, after); i >= 0 {
		at = i + 2
	}
	doc.Content = append(doc.Content[:at], append(pair, doc.Content[at:]...)...)
}

// encode writes the document back in its own syntax: JSON stays JSON.
func encode(root *yaml.Node, asJSON bool) ([]byte, error) {
	if asJSON {
		var v any
		if err := root.Decode(&v); err != nil {
			return nil, err
		}
		out, err := json.MarshalIndent(v, "", "  ")
		return append(out, '\n'), err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}
