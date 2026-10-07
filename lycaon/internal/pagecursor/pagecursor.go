// Package pagecursor seals list positions to a list kind, request scope,
// engine process, and optional snapshot generation.
package pagecursor

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxEncodedBytes = 4096

var (
	tokenKey        = randomBytes(32)
	processInstance = hex.EncodeToString(randomBytes(8))
)

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// ErrInvalid marks a malformed, tampered, or kind/scope-mismatched cursor.
var ErrInvalid = errors.New("invalid page cursor")

// ErrExpired marks a well-formed cursor whose engine process or snapshot
// generation is no longer served.
var ErrExpired = errors.New("page cursor generation expired")

// frameVersion prefixes every sealed payload, so a token never begins with
// eyJ, the base64 of a JSON object that secret detectors match.
const frameVersion byte = 1

// envelope is the framed, sealed cursor contents.
type envelope struct {
	Kind       string          `json:"k"`
	Scope      string          `json:"s"`
	Process    string          `json:"p"`
	Generation uint64          `json:"g,omitempty"`
	Value      json.RawMessage `json:"v"`
}

// Kind names one list. Two lists never share a kind.
type Kind string

// Scope joins the request inputs that shape a list into one binding value.
// Parts are length-prefixed, so no separator inside a part can alias another split.
func Scope(parts ...string) string {
	var b strings.Builder
	var n [binary.MaxVarintLen64]byte
	for _, part := range parts {
		b.Write(n[:binary.PutUvarint(n[:], uint64(len(part)))])
		b.WriteString(part)
	}
	return b.String()
}

// Codec seals positions of type P for one list kind.
type Codec[P any] struct {
	kind Kind
}

// For declares the codec for one list kind.
func For[P any](kind Kind) Codec[P] {
	if strings.TrimSpace(string(kind)) == "" {
		panic("pagecursor: empty kind")
	}
	return Codec[P]{kind: kind}
}

// Encode seals a position that is valid for as long as this engine process runs.
func (c Codec[P]) Encode(scope string, position P) (string, error) {
	return seal(string(c.kind), scope, 0, position)
}

// Decode opens a cursor minted by Encode for the same kind and scope.
func (c Codec[P]) Decode(token, scope string) (P, error) {
	var position P
	env, err := open(token, string(c.kind), scope, &position)
	if err != nil {
		return position, err
	}
	if env.Generation != 0 {
		var zero P
		return zero, ErrInvalid
	}
	return position, nil
}

// EncodeAt seals a position inside one published snapshot generation.
func (c Codec[P]) EncodeAt(scope string, generation uint64, position P) (string, error) {
	if generation == 0 {
		return "", fmt.Errorf("encode page cursor: %w: generation must be published", ErrInvalid)
	}
	return seal(string(c.kind), scope, generation, position)
}

// DecodeAt opens a cursor minted by EncodeAt. retained reports whether the list
// still serves a generation; a cursor for an unserved generation is ErrExpired.
func (c Codec[P]) DecodeAt(token, scope string, retained func(generation uint64) bool) (uint64, P, error) {
	var position P
	env, err := open(token, string(c.kind), scope, &position)
	if err != nil {
		return 0, position, err
	}
	var zero P
	if env.Generation == 0 {
		return 0, zero, ErrInvalid
	}
	if !retained(env.Generation) {
		return 0, zero, ErrExpired
	}
	return env.Generation, position, nil
}

// Current is the retained predicate for a list that serves only its newest generation.
func Current(generation uint64) func(uint64) bool {
	return func(g uint64) bool { return g == generation }
}

func seal(kind, scope string, generation uint64, value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode page cursor value: %w", err)
	}
	raw, err := json.Marshal(envelope{
		Kind: kind, Scope: scopeDigest(scope), Process: processInstance, Generation: generation, Value: body,
	})
	if err != nil {
		return "", fmt.Errorf("encode page cursor envelope: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(append([]byte{frameVersion}, raw...))
	sigRaw := append([]byte{frameVersion}, sign(payload)...)
	token := payload + "." + hex.EncodeToString(sigRaw)
	if len(token) > maxEncodedBytes {
		return "", fmt.Errorf("encode page cursor: %w: %d bytes exceeds %d", ErrInvalid, len(token), maxEncodedBytes)
	}
	return token, nil
}

// open verifies the token and decodes its value into dst. A token that fails
// signature checks is ErrExpired only when its readable envelope names this
// kind and scope from another engine process: a cursor from a previous run.
func open(token, kind, scope string, dst any) (envelope, error) {
	var env envelope
	if token == "" || len(token) > maxEncodedBytes {
		return env, ErrInvalid
	}
	payload, signatureText, ok := strings.Cut(token, ".")
	if !ok || payload == "" || signatureText == "" || strings.Contains(signatureText, ".") {
		return env, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(raw) == 0 || raw[0] != frameVersion || decodeStrict(raw[1:], &env) != nil || env.Kind != kind || env.Scope != scopeDigest(scope) {
		return envelope{}, ErrInvalid
	}
	sigRaw, err := hex.DecodeString(signatureText)
	if err != nil || len(sigRaw) == 0 || sigRaw[0] != frameVersion || !hmac.Equal(sigRaw[1:], sign(payload)) {
		if env.Process != "" && env.Process != processInstance {
			return envelope{}, ErrExpired
		}
		return envelope{}, ErrInvalid
	}
	if len(env.Value) == 0 || decodeStrict(env.Value, dst) != nil {
		return envelope{}, ErrInvalid
	}
	return env, nil
}

func sign(payload string) []byte {
	mac := hmac.New(sha256.New, tokenKey)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing cursor value")
	}
	return nil
}

func scopeDigest(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:12])
}
