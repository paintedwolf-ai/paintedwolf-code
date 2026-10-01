// Package projectignore reads and edits the project's shared ignore document.
package projectignore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"gopkg.in/yaml.v3"
)

const FileName = settingsoverlay.BasenameIgnores
const Version = 1
const MaxBytes = 1 << 20

var ErrConflict = errors.New("ignore file changed; reload before saving")
var ErrInvalid = errors.New("invalid ignore document")
var editMu sync.Mutex

func Path(root string) string { return filepath.Join(settingsoverlay.Dir(root), FileName) }

// Parse validates the envelope without interpreting either section's entries.
func Parse(data []byte) (*yaml.Node, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: file exceeds 1 MiB", ErrInvalid)
	}
	if data == nil {
		data = []byte("version: 1\nfindings: []\nsecrets: []\n")
	}
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: expected one YAML document", ErrInvalid)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: expected a mapping", ErrInvalid)
	}
	seen := map[string]bool{}
	for i := 0; i < len(doc.Content[0].Content); i += 2 {
		key, value := doc.Content[0].Content[i], doc.Content[0].Content[i+1]
		if seen[key.Value] {
			return nil, fmt.Errorf("%w: duplicate field %s", ErrInvalid, key.Value)
		}
		seen[key.Value] = true
		switch key.Value {
		case "version":
			var v int
			if err := value.Decode(&v); err != nil || value.Tag != "!!int" || v != Version {
				return nil, fmt.Errorf("%w: unsupported version", ErrInvalid)
			}
		case "findings", "secrets":
		default:
			return nil, fmt.Errorf("%w: unknown field %s", ErrInvalid, key.Value)
		}
	}
	if !seen["version"] {
		return nil, fmt.Errorf("%w: version is required", ErrInvalid)
	}
	return &doc, nil
}

func Section(doc *yaml.Node, name string) (*yaml.Node, error) {
	root := doc.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != name {
			continue
		}
		node := root.Content[i+1]
		if node.Tag == "!!null" {
			node.Kind, node.Tag, node.Value = yaml.SequenceNode, "!!seq", ""
		}
		if node.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%w: %s must be a sequence", ErrInvalid, name)
		}
		return node, nil
	}
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, node)
	return node, nil
}

func Read(root string) ([]byte, error) {
	if err := settingsoverlay.CheckFormat(root); err != nil {
		return nil, err
	}
	// Policy reads stay within the project root.
	f, err := fseffect.OpenRead(fseffect.Location{Root: root, Rel: settingsoverlay.Rel(FileName)})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: file exceeds 1 MiB", ErrInvalid)
	}
	return data, nil
}

// Edit preserves sibling sections and comments, and rejects concurrent file changes.
func Edit(root, section string, change func(*yaml.Node) error) error {
	editMu.Lock()
	defer editMu.Unlock()
	before, err := Read(root)
	if err != nil {
		return err
	}
	doc, err := Parse(before)
	if err != nil {
		return err
	}
	list, err := Section(doc, section)
	if err != nil {
		return err
	}
	if err := change(list); err != nil {
		return err
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if out.Len() > MaxBytes {
		return fmt.Errorf("%w: file exceeds 1 MiB", ErrInvalid)
	}
	if err := settingsoverlay.EnsureCurrentFormat(root); err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: settingsoverlay.Rel(FileName)}, Source: bytes.NewReader(out.Bytes()), Mode: 0o644, PreserveMode: true, DirMode: 0o750,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			f, e := target.Open()
			var current []byte
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			if e == nil {
				defer func() { _ = f.Close() }()
				current, e = io.ReadAll(io.LimitReader(f, MaxBytes+1))
				if e != nil {
					return e
				}
			}
			if !bytes.Equal(before, current) {
				return ErrConflict
			}
			if err := settingsoverlay.CheckFormat(root); err != nil {
				return err
			}
			format, err := settingsoverlay.ReadFormat(root)
			if err != nil {
				return err
			}
			if format < settingsoverlay.MaxFormat {
				return settingsoverlay.ErrFormatInvalid
			}
			return nil
		},
	})
	return err
}

func Digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// SectionDigest keeps unrelated section edits out of derived scanner caches.
func SectionDigest(data []byte, section string) string {
	doc, err := Parse(data)
	if err != nil {
		return Digest(data)
	}
	node, err := Section(doc, section)
	if err != nil {
		return Digest(data)
	}
	content, err := yaml.Marshal(node)
	if err != nil {
		return Digest(data)
	}
	return Digest(content)
}
