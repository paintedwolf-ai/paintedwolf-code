package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed effect_permit_identity.go
var effectPermitIdentityImplementation []byte

func migrateEffectPermitIdentity(ctx context.Context, tx *sql.Tx) error {
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,payload_json FROM checkpoints
 WHERE kind='tool_approval' AND payload_json IS NOT NULL AND payload_json<>''
 AND id>? ORDER BY id LIMIT 32`, after)
		if err != nil {
			return err
		}
		type update struct {
			id      string
			payload []byte
		}
		batch := []update{}
		for rows.Next() {
			var id string
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			converted, err := structuredEffectPermitPayload(raw)
			if err != nil {
				rows.Close()
				return fmt.Errorf("checkpoint authority format: %w", err)
			}
			batch = append(batch, update{id, converted})
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if len(batch) == 0 {
			return nil
		}
		for _, item := range batch {
			if item.payload != nil {
				if _, err := tx.ExecContext(ctx, `UPDATE checkpoints SET payload_json=? WHERE id=?`, string(item.payload), item.id); err != nil {
					return err
				}
			}
		}
		after = batch[len(batch)-1].id
	}
}

// Revision4 contains only structured once descriptors. Make that empty child
// scope explicit without manufacturing an execution identity or changing plan
// ids, semantic action digests, recorded options or grant lifetimes.
func structuredEffectPermitPayload(raw []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, fmt.Errorf("payload must be an object")
	}
	encoded, ok := payload["approval_plan"]
	if !ok {
		return nil, nil
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &plan); err != nil || plan == nil {
		return nil, fmt.Errorf("approval plan must be an object")
	}
	var options []map[string]json.RawMessage
	if err := json.Unmarshal(plan["options"], &options); err != nil || options == nil {
		return nil, fmt.Errorf("approval options must be an array")
	}
	changed := false
	for _, option := range options {
		var authority []map[string]json.RawMessage
		if err := json.Unmarshal(option["authority"], &authority); err != nil || authority == nil {
			return nil, fmt.Errorf("authority must be an array")
		}
		for _, delta := range authority {
			var kind string
			if err := json.Unmarshal(delta["kind"], &kind); err != nil {
				return nil, fmt.Errorf("authority kind must be a string")
			}
			if !knownEffectAuthorityKind(kind) {
				return nil, fmt.Errorf("unknown authority kind")
			}
			for key := range delta {
				if !knownEffectAuthorityField(key) {
					return nil, fmt.Errorf("unknown authority field")
				}
			}
			if kind != "socket_permit" && kind != "direct_ip_permit" {
				if _, present := delta["execution_id"]; present {
					return nil, fmt.Errorf("unrecognized child identity on retained authority")
				}
				continue
			}
			for _, key := range []string{"session_id", "tool_call_id", "action_digest"} {
				var identity string
				if err := json.Unmarshal(delta[key], &identity); err != nil || identity == "" {
					return nil, fmt.Errorf("retained once authority identity is incomplete")
				}
			}
			if prior, present := delta["execution_id"]; present && string(prior) != `""` {
				return nil, fmt.Errorf("revision4 cannot assert native child authority")
			}
			delta["execution_id"] = json.RawMessage(`""`)
			changed = true
		}
		if changed {
			encoded, err := json.Marshal(authority)
			if err != nil {
				return nil, err
			}
			option["authority"] = encoded
		}
	}
	if !changed {
		return nil, nil
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	plan["options"] = encoded
	encoded, err = json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	payload["approval_plan"] = encoded
	return json.Marshal(payload)
}

func knownEffectAuthorityKind(kind string) bool {
	switch kind {
	case "current_action", "generic_grant", "socket_permit", "socket_chat", "direct_ip_permit", "direct_ip_chat", "write_root_chat", "read_path_chat", "local_listen_chat", "loopback_connect_chat", "granted_path", "ask_quiet", "trust_destination":
		return true
	}
	return false
}
func knownEffectAuthorityField(key string) bool {
	switch key {
	case "kind", "grant", "chat_session_id", "session_id", "tool_call_id", "execution_id", "action_digest", "sockets", "direct_ip_lease", "write_roots", "read_paths", "listen_ports", "connect_ports", "granted_path", "ask_quiet", "trust_destination", "ttl_seconds":
		return true
	}
	return false
}
