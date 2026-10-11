package sessioncontracts

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
)

func TestComposerSecretProtectsSelectionAtChatScope(t *testing.T) {
	srv, secrets, _ := contractfixture.NewManagedSecretServer(t)
	operationID := uuid.NewString()
	body := contractfixture.CallSecrets(t, srv, http.MethodPost, "/v1/sessions/root-chat/composer-secrets", map[string]any{
		"name": "Relay token", "purpose": "authenticates the staging relay",
		"secret_value": "relay-token-value-1", "operation_id": operationID,
	}, http.StatusCreated)
	secret := contractfixture.DecodeSecret(t, body)
	if secret.Scope != "chat" || secret.Origin != "composer_marked" || secret.ChatSessionID == nil || *secret.ChatSessionID != "root-chat" {
		t.Fatalf("composer secret metadata = %+v", secret)
	}
	id := contractfixture.ReferenceID(t, secret.Reference)
	versions, err := secrets.List(t.Context(), testdbseed.DefaultProjectID, "root-chat")
	if err != nil || len(versions) != 1 || !strings.Contains(versions[0].Reference, id) {
		t.Fatalf("visible composer secret = %+v, err=%v", versions, err)
	}
}

// Selections below the screening minimum are refused before creation.

func TestComposerSecretRefusesSelectionBelowTheScreenFloor(t *testing.T) {
	srv, secrets, _ := contractfixture.NewManagedSecretServer(t)
	contractfixture.CallSecrets(t, srv, http.MethodPost, "/v1/sessions/root-chat/composer-secrets", map[string]any{
		"name": "PIN", "purpose": "one-character test credential",
		"secret_value": "x", "operation_id": uuid.NewString(),
	}, http.StatusBadRequest)
	versions, err := secrets.List(t.Context(), testdbseed.DefaultProjectID, "root-chat")
	if err != nil || len(versions) != 0 {
		t.Fatalf("a sub-floor selection was minted: %+v, err=%v", versions, err)
	}
}
