package nativegen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tokenizer decodes vocabulary token IDs from a checkpoint's tokenizer.json.
type Tokenizer struct {
	pieces  map[uint32]string
	special map[uint32]bool
}

type tokenizerFile struct {
	AddedTokens []struct {
		ID      uint32 `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	} `json:"added_tokens"`
	Model struct {
		Type  string          `json:"type"`
		Vocab json.RawMessage `json:"vocab"`
	} `json:"model"`
}

// LoadTokenizer reads and parses tokenizer.json from modelDir.
func LoadTokenizer(modelDir string) (*Tokenizer, error) {
	if strings.TrimSpace(modelDir) == "" {
		return nil, fmt.Errorf("nativegen: model directory is empty")
	}
	path := filepath.Join(modelDir, "tokenizer.json")
	raw, err := os.ReadFile(path) // #nosec G304 -- checkpoint path resolved from model store or explicit config
	if err != nil {
		return nil, fmt.Errorf("nativegen: read tokenizer: %w", err)
	}
	return ParseTokenizerJSON(raw)
}

// ParseTokenizerJSON parses a HuggingFace tokenizer.json payload.
func ParseTokenizerJSON(raw []byte) (*Tokenizer, error) {
	var tf tokenizerFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return nil, fmt.Errorf("nativegen: parse tokenizer.json: %w", err)
	}
	t := &Tokenizer{
		pieces:  make(map[uint32]string),
		special: make(map[uint32]bool),
	}
	if len(tf.Model.Vocab) > 0 {
		var vocabMap map[string]uint32
		if err := json.Unmarshal(tf.Model.Vocab, &vocabMap); err == nil {
			for piece, id := range vocabMap {
				t.pieces[id] = piece
			}
		} else {
			var unigramList [][]any
			if err := json.Unmarshal(tf.Model.Vocab, &unigramList); err != nil {
				return nil, fmt.Errorf("nativegen: unsupported model.vocab shape: %w", err)
			}
			for id, entry := range unigramList {
				if len(entry) > 0 {
					if piece, ok := entry[0].(string); ok {
						t.pieces[uint32(id)] = piece
					}
				}
			}
		}
	}
	for _, added := range tf.AddedTokens {
		t.pieces[added.ID] = added.Content
		if added.Special {
			t.special[added.ID] = true
		}
	}
	if len(t.pieces) == 0 {
		return nil, fmt.Errorf("nativegen: tokenizer.json contains no vocabulary entries")
	}
	return t, nil
}

// DecodeToken decodes a single token ID without cross-token byte buffering.
func (t *Tokenizer) DecodeToken(id uint32) (string, bool) {
	if t == nil {
		return "", false
	}
	piece, ok := t.pieces[id]
	if !ok {
		return "", false
	}
	if b, isByte := parseByteFallbackToken(piece); isByte {
		return string([]byte{b}), true
	}
	if t.special[id] || isControlDelimiterPiece(piece) {
		return piece, true
	}
	return strings.ReplaceAll(piece, "▁", " "), true
}

// StreamDecoder performs stateful incremental token decoding across token boundaries.
type StreamDecoder struct {
	tok     *Tokenizer
	pending []byte
}

// NewStreamDecoder returns an incremental token decoder backed by t.
func (t *Tokenizer) NewStreamDecoder() *StreamDecoder {
	if t == nil {
		return nil
	}
	return &StreamDecoder{tok: t}
}

// Step feeds one token ID and returns any newly completed UTF-8 text.
func (d *StreamDecoder) Step(id uint32) (string, bool) {
	if d == nil || d.tok == nil {
		return "", false
	}
	piece, ok := d.tok.pieces[id]
	if !ok {
		return "", false
	}
	if b, isByte := parseByteFallbackToken(piece); isByte {
		d.pending = append(d.pending, b)
		return d.drainValidUTF8(), true
	}
	var out strings.Builder
	if len(d.pending) > 0 {
		out.WriteString(string(d.pending))
		d.pending = d.pending[:0]
	}
	if d.tok.special[id] || isControlDelimiterPiece(piece) {
		out.WriteString(piece)
	} else {
		out.WriteString(strings.ReplaceAll(piece, "▁", " "))
	}
	return out.String(), true
}

// Flush drains any buffered byte-fallback bytes at the end of a stream.
func (d *StreamDecoder) Flush() string {
	if d == nil || len(d.pending) == 0 {
		return ""
	}
	s := string(d.pending)
	d.pending = d.pending[:0]
	return s
}

func (d *StreamDecoder) drainValidUTF8() string {
	var out strings.Builder
	for len(d.pending) > 0 {
		if !utf8.FullRune(d.pending) {
			break
		}
		r, width := utf8.DecodeRune(d.pending)
		if r == utf8.RuneError && width == 1 {
			out.WriteRune(utf8.RuneError)
			d.pending = d.pending[1:]
			continue
		}
		out.WriteRune(r)
		d.pending = d.pending[width:]
	}
	return out.String()
}

func parseByteFallbackToken(piece string) (byte, bool) {
	if len(piece) != 6 || !strings.HasPrefix(piece, "<0x") || !strings.HasSuffix(piece, ">") {
		return 0, false
	}
	v, err := strconv.ParseUint(piece[3:5], 16, 8)
	if err != nil {
		return 0, false
	}
	return byte(v), true
}

func isControlDelimiterPiece(piece string) bool {
	return strings.HasPrefix(piece, "<|") || strings.HasSuffix(piece, "|>")
}
