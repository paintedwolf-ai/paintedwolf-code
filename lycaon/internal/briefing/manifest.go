// Package briefing generates and records explanations of files, actions,
// activities and workers from bounded host evidence.
package briefing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Inclusion states how an evidence item reached the model.
type Inclusion string

const (
	IncludedWhole     Inclusion = "included"
	IncludedTruncated Inclusion = "truncated"
	// Omitted items are listed for coverage but never sent.
	Omitted Inclusion = "omitted"
	// Unavailable items had no readable or immutable form.
	Unavailable Inclusion = "unavailable"
)

// Role names what an item contributes to the question.
type Role string

const (
	RoleGoal         Role = "goal"
	RoleAction       Role = "action"
	RoleObservation  Role = "observation"
	RoleStructure    Role = "structure"
	RoleVerification Role = "verification"
	RoleContext      Role = "context"
)

// Item is one bounded, redacted piece of evidence. Text is the exact excerpt
// submitted; Digest identifies it for hashing and is derived from Text.
type Item struct {
	Key        string    `json:"key"`
	Role       Role      `json:"role"`
	SourceKind string    `json:"source_kind"`
	Ref        string    `json:"ref,omitempty"`
	Revision   string    `json:"revision,omitempty"`
	Inclusion  Inclusion `json:"inclusion"`
	Reason     string    `json:"reason,omitempty"`
	Text       string    `json:"-"`
	Digest     string    `json:"digest,omitempty"`
}

// Template identifies the prompt contract a section was asked under.
type Template struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Manifest is everything a section result depends on. Its hash is the
// result's identity, so equal material shares one result across targets.
type Manifest struct {
	ProfileID      string   `json:"profile_id"`
	ProfileVersion string   `json:"profile_version"`
	SectionID      string   `json:"section_id"`
	Template       Template `json:"template"`
	Contract       Contract `json:"contract"`
	Items          []Item   `json:"items"`
}

// Sealed fills each item's digest from its normalized text. Callers seal a
// manifest once its items are final.
func (m Manifest) Sealed() Manifest {
	items := make([]Item, len(m.Items))
	for i, item := range m.Items {
		item.Text = norm.NFC.String(item.Text)
		if item.Inclusion == IncludedWhole || item.Inclusion == IncludedTruncated {
			sum := sha256.Sum256([]byte(item.Text))
			item.Digest = hex.EncodeToString(sum[:])
		} else {
			item.Text, item.Digest = "", ""
		}
		items[i] = item
	}
	m.Items = items
	return m
}

// Canonical serializes the sealed manifest in a fixed field order with no
// insignificant whitespace.
func (m Manifest) Canonical() ([]byte, error) {
	sealed := m.Sealed()
	for _, item := range sealed.Items {
		if strings.TrimSpace(item.Key) == "" || item.Role == "" || item.SourceKind == "" || item.Inclusion == "" {
			return nil, fmt.Errorf("briefing manifest item %q is incomplete", item.Key)
		}
	}
	return json.Marshal(sealed)
}

// Hash is the lowercase hex SHA-256 of the canonical manifest.
func (m Manifest) Hash() (string, error) {
	canonical, err := m.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// SectionHash pairs a section with the manifest hash that produced it.
type SectionHash struct {
	SectionID    string `json:"section_id"`
	ManifestHash string `json:"manifest_hash"`
}

// RecordID identifies an evidence record by its profile and ordered sections.
func RecordID(profileID, profileVersion string, sections []SectionHash) string {
	canonical, err := json.Marshal(struct {
		ProfileID      string        `json:"profile_id"`
		ProfileVersion string        `json:"profile_version"`
		Sections       []SectionHash `json:"sections"`
	}{profileID, profileVersion, sections})
	if err != nil {
		panic(fmt.Errorf("encode briefing record identity: %w", err))
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
