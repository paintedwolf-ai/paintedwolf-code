package settingsoverlay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

// EnsureCurrentFormat publishes the marker before a current-format artifact is written.
func EnsureCurrentFormat(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return fmt.Errorf("%w: an overlay root is required", ErrFormatInvalid)
	}
	path := FormatPath(root)
	before, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	doc := yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	if existed {
		format, err := parseFormat(before)
		if err != nil {
			return err
		}
		if format > MaxFormat {
			return ErrFormatTooNew
		}
		if format == MaxFormat {
			return nil
		}
		if err := yaml.Unmarshal(before, &doc); err != nil {
			return fmt.Errorf("%w: parse %s: %w", ErrFormatInvalid, path, err)
		}
	}
	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: %s must be a mapping", ErrFormatInvalid, path)
	}
	var marker *yaml.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "overlay_format" {
			marker = mapping.Content[i+1]
			break
		}
	}
	if marker == nil {
		marker = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int"}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "overlay_format"}, marker)
	}
	marker.Kind, marker.Tag, marker.Value = yaml.ScalarNode, "!!int", fmt.Sprint(MaxFormat)
	var body bytes.Buffer
	encoder := yaml.NewEncoder(&body)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: Rel(FormatFileName)}, Source: &body,
		Mode: 0o644, PreserveMode: true, DirMode: 0o750,
		BeforeCommit: func(target fseffect.Target, _ fseffect.Result) error {
			return checkFormatPreimage(target, before, existed)
		},
	})
	if err != nil {
		return err
	}
	return CheckFormat(root)
}

// checkFormatPreimage detects edits made since the marker was read.
func checkFormatPreimage(target fseffect.Target, before []byte, existed bool) error {
	file, err := target.Open()
	if !existed && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	after, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if !existed || !bytes.Equal(before, after) {
		return fmt.Errorf("overlay format changed while preparing the update; retry")
	}
	return nil
}
