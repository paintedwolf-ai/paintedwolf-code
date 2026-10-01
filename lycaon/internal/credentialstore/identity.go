package credentialstore

import (
	"encoding/json"
	"fmt"

	"filippo.io/age"
	"github.com/google/uuid"
)

func generateIdentityDocument() (identityDocument, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return identityDocument{}, fmt.Errorf("generate credential vault identity: %w", err)
	}
	return identityDocument{
		Format: identityFormat, VaultID: uuid.NewString(), Identity: identity.String(),
	}, nil
}

func encodeIdentityDocument(doc identityDocument) ([]byte, error) {
	data, err := json.Marshal(doc) // #nosec G117 -- callers protect this identity before persistence.
	if err != nil {
		return nil, fmt.Errorf("encode credential vault identity: %w", err)
	}
	return data, nil
}

func decodeIdentityDocument(data []byte) (identityDocument, error) {
	var doc identityDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return identityDocument{}, fmt.Errorf("%w: parse identity: %w", ErrVaultCorrupt, err)
	}
	return doc, nil
}
