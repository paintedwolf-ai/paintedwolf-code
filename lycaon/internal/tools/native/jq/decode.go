package jq

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
	"gopkg.in/op/go-logging.v1"

	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

const maxDocumentDepth = 32

var (
	errDocumentDepthExceeded = errors.New("document depth exceeded")
	silenceYqlibLogging      sync.Once
)

func parseFormat(explicit string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "", "auto":
		return "auto", nil
	case "json", "yaml", "toml":
		return strings.ToLower(strings.TrimSpace(explicit)), nil
	default:
		return "", safecmd.Reject("JQ_FORMAT_INVALID", map[string]any{
			"format":  explicit,
			"allowed": []string{"auto", "json", "yaml", "toml"},
		})
	}
}

func detectFormat(path, explicit string) (string, error) {
	resolved, err := parseFormat(explicit)
	if err != nil {
		return "", err
	}
	if resolved != "auto" {
		return resolved, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json", nil
	case ".yaml", ".yml":
		return "yaml", nil
	case ".toml":
		return "toml", nil
	default:
		return "auto", nil
	}
}

func decodeInputs(data []byte, format string) ([]any, string, error) {
	switch format {
	case "json":
		inputs, err := decodeJSONStream(data)
		if err != nil {
			return nil, "json", err
		}
		return inputs, "json", nil
	case "yaml":
		inputs, err := decodeYqlibDocuments(data, yqlib.NewYamlDecoder(yqlib.ConfiguredYamlPreferences))
		if err != nil {
			return nil, "yaml", err
		}
		return inputs, "yaml", nil
	case "toml":
		inputs, err := decodeYqlibDocuments(data, yqlib.NewTomlDecoder())
		if err != nil {
			return nil, "toml", err
		}
		return inputs, "toml", nil
	case "auto":
		if inputs, err := decodeJSONStream(data); err == nil {
			return inputs, "json", nil
		}
		if inputs, err := decodeYqlibDocuments(data, yqlib.NewYamlDecoder(yqlib.ConfiguredYamlPreferences)); err == nil {
			return inputs, "yaml", nil
		}
		if inputs, err := decodeYqlibDocuments(data, yqlib.NewTomlDecoder()); err == nil {
			return inputs, "toml", nil
		}
		return nil, "", errors.New("could not parse document as JSON, YAML, or TOML")
	default:
		return nil, "", fmt.Errorf("unsupported format %q", format)
	}
}

func decodeYqlibDocuments(data []byte, decoder yqlib.Decoder) ([]any, error) {
	silenceYqlibLogging.Do(func() {
		logging.SetLevel(logging.ERROR, "yq-lib")
	})
	docs, err := yqlib.ReadDocuments(bytes.NewReader(data), decoder)
	if err != nil {
		return nil, err
	}
	if docs == nil || docs.Len() == 0 {
		return nil, errors.New("no document in file")
	}
	var inputs []any
	for e := docs.Front(); e != nil; e = e.Next() {
		node, ok := e.Value.(*yqlib.CandidateNode)
		if !ok || node == nil {
			continue
		}
		if candidateDepth(node) > maxDocumentDepth {
			return nil, errDocumentDepthExceeded
		}
		v, err := candidateToAny(node)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, v)
	}
	if len(inputs) == 0 {
		return nil, errors.New("no document in file")
	}
	return inputs, nil
}

func candidateDepth(n *yqlib.CandidateNode) int {
	if n == nil {
		return 0
	}
	maxChild := 0
	for _, c := range n.Content {
		if d := candidateDepth(c); d > maxChild {
			maxChild = d
		}
	}
	return maxChild + 1
}

func candidateToAny(node *yqlib.CandidateNode) (any, error) {
	raw, err := node.MarshalJSON()
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, io.EOF
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func normalizedFormatNote(parsedAs string) string {
	switch parsedAs {
	case "json":
		return ""
	default:
		return fmt.Sprintf(
			"Parsed as %s — values[] are normalized (decoded structure), not source-verbatim bytes.",
			parsedAs,
		)
	}
}

func decodeJSONStream(data []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var out []any
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("no JSON value in document")
	}
	return out, nil
}
