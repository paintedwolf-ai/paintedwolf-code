// Package jsonblob encodes and decodes versioned JSON envelopes for database storage.
package jsonblob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrBlobTooNew means the blob's version is newer than this build accepts.
	ErrBlobTooNew = errors.New("json blob written by a newer version")
	// ErrBlobTooOld means the blob is older than the current/current-1 window.
	ErrBlobTooOld = errors.New("json blob older than the supported window")
	// ErrBlobUnversioned means the blob is valid JSON with no "v" field.
	ErrBlobUnversioned = errors.New("json blob carries no version")
)

// Marshal encodes v with the version field inlined. It fails if v marshals to
// anything but a JSON object, or if the payload already carries a "v" key.
func Marshal(v any, version int) ([]byte, error) {
	if version < 1 {
		return nil, fmt.Errorf("jsonblob: version must be >= 1, got %d", version)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jsonblob: marshal payload: %w", err)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, fmt.Errorf("jsonblob: payload must marshal to a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("jsonblob: decode payload object: %w", err)
	}
	if _, exists := fields["v"]; exists {
		return nil, fmt.Errorf("jsonblob: payload already carries a \"v\" key")
	}

	verRaw, err := json.Marshal(version)
	if err != nil {
		return nil, fmt.Errorf("jsonblob: marshal version: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	buf.WriteString(`"v":`)
	buf.Write(verRaw)
	// Splice the payload's members after "v" in the order json.Marshal wrote them.
	inner := bytes.TrimSpace(raw[1 : len(raw)-1])
	if len(inner) > 0 {
		buf.WriteByte(',')
		buf.Write(inner)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Version reads the envelope version without decoding the payload.
// ok is false for valid JSON that carries no "v" field.
func Version(data []byte) (version int, ok bool) {
	var probe struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return 0, false
	}
	if probe.V == nil {
		return 0, false
	}
	return *probe.V, true
}

// Unmarshal decodes into dst when the blob's version is current or current-1.
// A newer version returns ErrBlobTooNew; older than current-1 returns
// ErrBlobTooOld; an absent version returns ErrBlobUnversioned.
func Unmarshal(data []byte, dst any, current int) error {
	if current < 1 {
		return fmt.Errorf("jsonblob: current must be >= 1, got %d", current)
	}
	ver, ok := Version(data)
	if !ok {
		// Distinguish invalid JSON from a missing version.
		var discard any
		if err := json.Unmarshal(data, &discard); err != nil {
			return fmt.Errorf("jsonblob: %w", err)
		}
		return ErrBlobUnversioned
	}
	if ver > current {
		return fmt.Errorf("%w: blob v=%d current=%d", ErrBlobTooNew, ver, current)
	}
	if ver < current-1 {
		return fmt.Errorf("%w: blob v=%d current=%d", ErrBlobTooOld, ver, current)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("jsonblob: decode payload: %w", err)
	}
	return nil
}
