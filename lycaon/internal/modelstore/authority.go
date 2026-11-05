package modelstore

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// Authority verifies model manifests with the host's pinned release keys.
type Authority struct {
	keys map[string]ed25519.PublicKey
}

func NewAuthority(keys map[string]ed25519.PublicKey) (*Authority, error) {
	a := &Authority{keys: make(map[string]ed25519.PublicKey, len(keys))}
	for id, key := range keys {
		if !nameShape.MatchString(id) || len(key) != ed25519.PublicKeySize {
			return nil, errors.New("modelstore: invalid authority key")
		}
		a.keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return a, nil
}

// SigningBytes binds the signature to the model-manifest domain and field order.
func SigningBytes(m Manifest) ([]byte, error) {
	if err := validate(m); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(m)
	return append([]byte("paintedwolf-model-manifest/1\n"), raw...), err
}

func (a *Authority) Verify(signed SignedManifest) error {
	key, ok := a.keys[signed.KeyID]
	if !ok {
		return errors.New("modelstore: unknown signing key")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(signed.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("modelstore: invalid manifest signature encoding")
	}
	raw, err := SigningBytes(signed.Manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, raw, signature) {
		return errors.New("modelstore: manifest signature rejected")
	}
	return nil
}
