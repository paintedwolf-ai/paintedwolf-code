package migrations

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed effect_permit_identity.go
var effectPermitIdentityImplementation []byte

// Revision5 stores native approval authority losslessly beside its screened
// JSON display in the same authority rows. Revision4 holds no native authority,
// so every retained row keeps the empty structured format.
var nativeAuthorityColumns = []string{"checkpoints", "approval_operations", "chat_grants"}

const nativeAuthorityColumnsDDL = `ALTER TABLE %[1]s ADD COLUMN authority_format TEXT NOT NULL DEFAULT '' CHECK (authority_format IN ('', 'native_approval_authority_v1'));
ALTER TABLE %[1]s ADD COLUMN authority_digest TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_digest = '') AND (authority_digest = '' OR length(authority_digest) = 64));
ALTER TABLE %[1]s ADD COLUMN authority_body TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_body = '') AND (authority_body = '' OR json_valid(authority_body)));`

func migrateEffectPermitIdentity(ctx context.Context, tx *sql.Tx) error {
	for _, table := range nativeAuthorityColumns {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(nativeAuthorityColumnsDDL, table)); err != nil {
			return fmt.Errorf("native authority columns on %s: %w", table, err)
		}
	}
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
	if _, present := payload["native_arguments_representation"]; present {
		return nil, fmt.Errorf("revision4 cannot assert native argument representation")
	}
	encoded, ok := payload["approval_plan"]
	if !ok {
		return nil, nil
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &plan); err != nil || plan == nil {
		return nil, fmt.Errorf("approval plan must be an object")
	}
	for key := range plan {
		if !knownEffectPlanField(key) {
			return nil, fmt.Errorf("unknown approval plan field")
		}
	}
	for _, key := range []string{"id", "action_digest", "stage", "recommended_option_id"} {
		if !retainedIdentity(plan[key]) {
			return nil, fmt.Errorf("approval plan identity is incomplete")
		}
	}
	var options []map[string]json.RawMessage
	if err := json.Unmarshal(plan["options"], &options); err != nil || len(options) == 0 {
		return nil, fmt.Errorf("approval options must be an array")
	}
	changed := false
	for _, option := range options {
		for key := range option {
			if !knownEffectOptionField(key) {
				return nil, fmt.Errorf("unknown approval option field")
			}
		}
		if !retainedIdentity(option["id"]) {
			return nil, fmt.Errorf("approval option identity is incomplete")
		}
		var authority []map[string]json.RawMessage
		if err := json.Unmarshal(option["authority"], &authority); err != nil || len(authority) == 0 {
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
				if err := retainedOnceBoundary(kind, delta); err != nil {
					return nil, err
				}
				if _, present := delta["execution_id"]; present {
					return nil, fmt.Errorf("unrecognized child identity on retained authority")
				}
				continue
			}
			for _, key := range []string{"session_id", "tool_call_id", "action_digest"} {
				if !retainedIdentity(delta[key]) {
					return nil, fmt.Errorf("retained once authority identity is incomplete")
				}
			}
			if err := retainedOnceBoundary(kind, delta); err != nil {
				return nil, err
			}
			if _, present := delta["execution_id"]; present {
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

// These field sets freeze revision4's stored authority envelope. Display-only
// payload annotations outside approval_plan remain untouched.
func knownEffectPlanField(key string) bool {
	switch key {
	case "id", "action_digest", "stage", "subject", "presentation", "reasons", "options", "directory_scopes", "recommended_option_id", "held":
		return true
	}
	return false
}
func knownEffectOptionField(key string) bool {
	switch key {
	case "id", "kind", "rung", "scope", "group", "directory_scope", "title", "coverage", "expires_when", "reask_when", "decision_action", "disabled", "note", "authority":
		return true
	}
	return false
}
func retainedIdentity(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\x01")
}

// Freeze the changed boundary payloads at revision4's exact stored shapes.
// This is format recognition only; existing capability owners still decide
// whether a restored descriptor is complete, current and usable.
func retainedOnceBoundary(kind string, delta map[string]json.RawMessage) error {
	decode := func(raw json.RawMessage, target any) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("unrecognized retained boundary payload: %w", err)
		}
		return nil
	}
	if kind == "socket_permit" {
		var sockets []struct {
			ApprovedPath string `json:"approved_path"`
			ResolvedPath string `json:"resolved_path"`
		}
		if err := decode(delta["sockets"], &sockets); err != nil {
			return err
		}
		if len(sockets) == 0 {
			return fmt.Errorf("retained socket boundary is missing")
		}
		return nil
	}
	var lease struct {
		ActionDigest          string
		RequestDigest         string
		ConfinementDigest     string
		ChatConfinementDigest string
		DeclaredDestinations  []string
		CommandSummary        string
	}
	if err := decode(delta["direct_ip_lease"], &lease); err != nil {
		return err
	}
	if lease.ActionDigest == "" || lease.RequestDigest == "" || lease.ConfinementDigest == "" {
		return fmt.Errorf("retained direct-IP boundary identity is incomplete")
	}
	return nil
}
