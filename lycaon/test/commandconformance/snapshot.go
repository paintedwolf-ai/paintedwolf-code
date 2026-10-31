package commandconformance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"unicode/utf8"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

// Snapshots are the stored reference results of the corpus.
type Snapshots struct {
	// Userlands records the images the results came from.
	Userlands map[referenceuserland.ID]Image `json:"userlands"`
	// Cases holds, per case, the result of each userland it compares with.
	Cases map[string]map[referenceuserland.ID]Snapshot `json:"cases"`
}

// Image identifies a verified userland image by its pinned base and recorded trees.
type Image struct {
	Base  string            `json:"base"`
	Trees map[string]string `json:"trees"`
}

// Snapshot is one userland's result for one case.
type Snapshot struct {
	// Input is the case's [Case.Input] for the userland when it was captured.
	Input  string           `json:"input"`
	Status int              `json:"status"`
	Stdout Text             `json:"stdout"`
	Stderr Text             `json:"stderr"`
	Files  map[string]*File `json:"files,omitempty"`
}

// File is the final state of a tracked path; nil when the path does not exist.
type File struct {
	Mode Mode   `json:"mode"`
	Data Text   `json:"data,omitempty"`
	Link string `json:"link,omitempty"`
}

// Entry is the file as a workspace seed entry.
func (f *File) Entry() referenceuserland.Entry {
	return referenceuserland.Entry{Mode: fs.FileMode(f.Mode), Data: f.Data, Link: f.Link}
}

// FileOf records a runner result entry.
func FileOf(e *referenceuserland.Entry) *File {
	if e == nil {
		return nil
	}
	return &File{Mode: Mode(e.Mode), Data: e.Data, Link: e.Link}
}

// Text is output that reads as a JSON string when it is UTF-8, and as
// {"base64": ...} when it is not.
type Text []byte

func (t Text) MarshalJSON() ([]byte, error) {
	if utf8.Valid(t) {
		return encodeJSON(string(t), "")
	}
	return encodeJSON(map[string]string{"base64": base64.StdEncoding.EncodeToString(t)}, "")
}

func (t *Text) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*t = Text(s)
		return nil
	}
	var encoded struct {
		Base64 string `json:"base64"`
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded.Base64)
	*t = decoded
	return err
}

// Mode reads as the ls-style mode string, such as "-rw-r--r--".
type Mode fs.FileMode

func (m Mode) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.String())
}

func (m *Mode) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if len(s) != 10 {
		return fmt.Errorf("mode %q is not a type and nine permission bits", s)
	}
	var mode fs.FileMode
	switch s[0] {
	case '-':
	case 'd':
		mode = fs.ModeDir
	case 'L':
		mode = fs.ModeSymlink
	default:
		return fmt.Errorf("mode %q has an unsupported type", s)
	}
	for i, c := range s[1:] {
		switch {
		case c == rune("rwxrwxrwx"[i]):
			mode |= 1 << (8 - i)
		case c != '-':
			return fmt.Errorf("mode %q has an unsupported permission", s)
		}
	}
	*m = Mode(mode)
	return nil
}

// LoadSnapshots reads the stored snapshots and refuses ones captured from images
// other than the manifest's.
func LoadSnapshots() (*Snapshots, error) {
	data, err := testdata.ReadFile(embedded(SnapshotsPath))
	if err != nil {
		return nil, err
	}
	manifest, err := referenceuserland.LoadManifest()
	if err != nil {
		return nil, err
	}
	s, err := decodeSnapshots(data)
	if err != nil {
		return nil, err
	}
	if !imagesEqual(s.Userlands, Images(manifest)) {
		return nil, fmt.Errorf("snapshots come from other reference images; run ./task conformance:capture")
	}
	return s, nil
}

func decodeSnapshots(data []byte) (*Snapshots, error) {
	var s Snapshots
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode snapshots: %w", err)
	}
	if s.Cases == nil {
		s.Cases = map[string]map[referenceuserland.ID]Snapshot{}
	}
	return &s, nil
}

// Images identifies the manifest's userland images.
func Images(m *referenceuserland.Manifest) map[referenceuserland.ID]Image {
	images := map[referenceuserland.ID]Image{}
	for id, u := range m.Userlands {
		images[id] = Image{Base: u.Base, Trees: maps.Clone(u.Trees)}
	}
	return images
}

func imagesEqual(a, b map[referenceuserland.ID]Image) bool {
	return maps.EqualFunc(a, b, func(x, y Image) bool { return x.Base == y.Base && maps.Equal(x.Trees, y.Trees) })
}

// For returns the stored result of each userland the case compares with, and
// refuses a missing one or one captured from a different input.
func (s *Snapshots) For(c Case, seed GitSeed) (map[referenceuserland.ID]Snapshot, error) {
	out := map[referenceuserland.ID]Snapshot{}
	for _, id := range c.References {
		snap, ok := s.Cases[c.ID][id]
		if !ok || snap.Input != c.Input(id, seed) {
			return nil, fmt.Errorf("the %s snapshot of case %s is missing or stale; run ./task conformance:capture -- %s", id, c.ID, c.ID)
		}
		out[id] = snap
	}
	return out, nil
}

// Encode renders snapshots in their canonical form.
func (s *Snapshots) Encode() ([]byte, error) {
	return encodeJSON(s, "  ")
}

// encodeJSON keeps <, > and & literal, since programs and their output are full of them.
func encodeJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", indent)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	if indent == "" {
		return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
	}
	return buf.Bytes(), nil
}

// Retain drops the snapshots of cases and userlands the corpus no longer compares with.
func (s *Snapshots) Retain(cases []Case) {
	wanted := map[string][]referenceuserland.ID{}
	for _, c := range cases {
		wanted[c.ID] = c.References
	}
	for id, byUserland := range s.Cases {
		refs, ok := wanted[id]
		if !ok {
			delete(s.Cases, id)
			continue
		}
		maps.DeleteFunc(byUserland, func(u referenceuserland.ID, _ Snapshot) bool { return !slices.Contains(refs, u) })
	}
}
