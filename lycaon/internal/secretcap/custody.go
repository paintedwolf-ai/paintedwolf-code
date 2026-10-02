package secretcap

import (
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/credentialstore"
)

// Custody says who supplied a protected value's bytes, and so what a release
// of them requires. It is recorded inside the encrypted vault entry when the
// bytes enter and never derived from metadata, because a process that can
// rewrite the metadata store must not be able to relabel a value.
type Custody string

const (
	// CustodyPerson: a person handed the bytes to the vault, and the vault may
	// hold the only copy. Every release needs that person's presence.
	CustodyPerson Custody = "person"
	// CustodyFile: a person marked bytes that already sit in a project file,
	// which governs them.
	CustodyFile Custody = "file"
	// CustodyChat: the host generated the bytes for one chat, which alone has
	// held them. The entry names that chat.
	CustodyChat Custody = "chat"
	// CustodyHost: the host generated or captured the bytes for the agent's
	// work beyond one chat.
	CustodyHost Custody = "host"
)

// Held reports whether releasing the value requires presence.
func (c Custody) Held() bool { return c == CustodyPerson }

// firstEntry is the vault entry for a capability's first value.
func firstEntry(req PutRequest) protectedValue {
	entry := protectedValue{Value: req.Value}
	switch req.Origin {
	case OriginAskUserResponse, OriginComposerMarked, OriginSettingsEntered, OriginDetected:
		entry.Custody = CustodyPerson
	case OriginFileMarked:
		entry.Custody = CustodyFile
	case OriginGenerated:
		entry.Custody = CustodyHost
		if req.Scope == ScopeChat {
			entry.Custody, entry.Chat = CustodyChat, req.ChatSessionID
		}
	default:
		entry.Custody = CustodyHost
	}
	return entry
}

// GeneratedFor reports whether the host generated these bytes for chat.
func (p protectedValue) GeneratedFor(chat string) bool {
	return p.Custody == CustodyChat && p.Chat != "" && p.Chat == chat
}

const protectedValueFormat = 1

// protectedValue is one vault entry: the bytes and who supplied them.
type protectedValue struct {
	Format  int     `json:"format"`
	Custody Custody `json:"custody"`
	// Chat names the chat a chat-custody value was generated for.
	Chat  string `json:"chat,omitempty"`
	Value string `json:"value"`
}

// vaultValues stores protected values in the managed-secret vault namespace,
// keyed by version id.
type vaultValues struct {
	store *credentialstore.Store
}

func (v vaultValues) put(versionID string, entry protectedValue) error {
	entry.Format = protectedValueFormat
	if entry.Custody != CustodyChat {
		entry.Chat = ""
	}
	encoded, err := json.Marshal(entry) // #nosec G117 -- age encrypts the vault document.
	if err != nil {
		return fmt.Errorf("encode protected value: %w", err)
	}
	return v.store.Set(versionID, string(encoded))
}

// get returns a well-formed entry. An entry of an unknown shape is refused
// without modification, so its value reports unavailable.
func (v vaultValues) get(versionID string) (protectedValue, bool) {
	raw, ok := v.store.Get(versionID)
	if !ok {
		return protectedValue{}, false
	}
	var entry protectedValue
	if err := json.Unmarshal([]byte(raw.Value()), &entry); err != nil {
		return protectedValue{}, false
	}
	switch {
	case entry.Format != protectedValueFormat, entry.Value == "":
		return protectedValue{}, false
	case entry.Custody != CustodyPerson && entry.Custody != CustodyFile && entry.Custody != CustodyChat && entry.Custody != CustodyHost:
		return protectedValue{}, false
	case (entry.Custody == CustodyChat) != (entry.Chat != ""):
		return protectedValue{}, false
	}
	return entry, true
}

func (v vaultValues) delete(versionID string) error { return v.store.Delete(versionID) }

func (v vaultValues) ids() []string { return v.store.IDs() }
