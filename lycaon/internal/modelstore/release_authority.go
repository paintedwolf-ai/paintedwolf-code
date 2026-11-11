package modelstore

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

const MaxAuthorityBytes = 16 << 10

// DecodeAuthority reads release keys from the application's trusted resources.
func DecodeAuthority(raw []byte) (*Authority, error) {
	invalid := errors.New("modelstore: invalid release authority")
	if len(raw) == 0 || len(raw) > MaxAuthorityBytes {
		return nil, invalid
	}
	var document struct {
		Format int `json:"format"`
		Keys   []struct {
			ID        string `json:"id"`
			PublicKey string `json:"public_key"`
		} `json:"keys"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF || document.Format != 1 || len(document.Keys) == 0 || len(document.Keys) > 16 {
		return nil, invalid
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	for _, entry := range document.Keys {
		key, err := base64.StdEncoding.Strict().DecodeString(entry.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize || keys[entry.ID] != nil {
			return nil, invalid
		}
		keys[entry.ID] = ed25519.PublicKey(key)
	}
	return NewAuthority(keys)
}
