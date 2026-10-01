package delegation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ErrOperationConflict rejects an operation_id already used with a different request.
var ErrOperationConflict = errors.New("delegation operation_id reused with a different request")

// CreateReceipt is an API create's idempotency key and the digest of the
// request it was made from. A store commits it with the delegation, so a
// repeated create answers the first one's delegation across restarts.
type CreateReceipt struct {
	OperationID string
	InputDigest string
}

// normalizeCreateRequest applies the host defaults a create request may omit.
func normalizeCreateRequest(req api.CreateDelegationRequest) api.CreateDelegationRequest {
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.Strategy == "" {
		req.Strategy = api.HuntStrategyFileBased
	}
	return req
}

// createReceipt digests a normalized request, so an omitted default and the
// same value given explicitly are one input.
func createReceipt(req api.CreateDelegationRequest) (CreateReceipt, error) {
	operationID := req.OperationID
	req.OperationID = ""
	raw, err := json.Marshal(req)
	if err != nil {
		return CreateReceipt{}, fmt.Errorf("encode delegation create: %w", err)
	}
	digest := sha256.Sum256(raw)
	return CreateReceipt{OperationID: operationID, InputDigest: hex.EncodeToString(digest[:])}, nil
}
