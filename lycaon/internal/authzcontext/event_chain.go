package authzcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/lycaon/lycaon/internal/db"
)

type eventChainPayload struct {
	ContextSeq int    `json:"context_seq"`
	Action     string `json:"action"`
	Outcome    string `json:"outcome"`
	ResolvedBy string `json:"resolved_by"`
	// ResolverPersonID binds the deciding person into the chain.
	ResolverPersonID string `json:"resolver_person_id"`
	ToolName         string `json:"tool_name"`
	RejectCode       string `json:"reject_code"`
	DetailJSON       string `json:"detail_json"`
	ConfigHash       string `json:"config_hash"`
}

func eventChainPayloadFromEvent(e Event) eventChainPayload {
	detail := e.DetailJSON
	if detail == "" {
		detail = "{}"
	}
	return eventChainPayload{
		ContextSeq:       e.ContextSeq,
		Action:           string(e.Action),
		Outcome:          string(e.Outcome),
		ResolvedBy:       string(e.ResolvedBy),
		ResolverPersonID: e.ResolverPersonID,
		ToolName:         e.ToolName,
		RejectCode:       e.RejectCode,
		DetailJSON:       detail,
		ConfigHash:       e.ConfigHash,
	}
}

// ComputeEventRowHash implements D2 row_hash for authz_events hash_version=1.
func ComputeEventRowHash(hashVersion int, sessionID string, seq int, ts string, payload eventChainPayload, prevHash string) (string, error) {
	canonical, err := CanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\n", hashVersion)
	h.Write([]byte(sessionID))
	h.Write([]byte{'\n'})
	h.Write([]byte(strconv.Itoa(seq)))
	h.Write([]byte{'\n'})
	h.Write([]byte(ts))
	h.Write([]byte{'\n'})
	h.Write(canonical)
	h.Write([]byte{'\n'})
	h.Write([]byte(prevHash))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func eventChainTimestamp(e Event) string {
	if e.RecordedAt.IsZero() {
		return ""
	}
	return db.FormatTime(e.RecordedAt)
}

func marshalEventDetail(d EventDetail) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
