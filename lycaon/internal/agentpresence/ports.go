package agentpresence

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// Span addresses part of a document text. Lines are 1-based and inclusive;
// characters are UTF-16 offsets within their line. Equal start and end
// positions address an insertion point.
type Span struct {
	StartLine, EndLine           int
	StartCharacter, EndCharacter *int
}

// Document identifies the editor document state an item was recorded against.
// The zero value means no document served the text.
type Document struct {
	ID       string
	Revision int64
}

// Target is a root-relative file in the project tree.
type Target struct {
	RootID, Path string
}

// Call identifies the tool call an item came from.
type Call struct {
	SessionID, ToolCallID, Tool string
}

// Read is text one call returned to the model, after host output limits.
type Read struct {
	Target
	Document Document
	Extent   api.AgentPresenceExtent
	Spans    []Span
	// ItemSpans counts the spans each entry of a paginated result contributed,
	// in result order, so a result trimmed to its first entries keeps only
	// theirs. Empty for a read that is one entry.
	ItemSpans []int
}

// Intent is a mutation the host resolved against the document state the agent read.
type Intent struct {
	Target
	ToPath    string
	Operation api.AgentIntentOperation
	Document  Document
	Extent    api.AgentPresenceExtent
	Spans     []Span
}

// DraftFile is one primary-tree file a worker job changes. Spans are the
// changed lines of the current file and are known once the draft is ready.
type DraftFile struct {
	Target
	Insertions, Deletions *int
	Spans                 []Span
}

// AnchoredSpan is a span as CRDT relative positions in its document epoch.
type AnchoredSpan struct {
	Anchor, Head []byte
	Expected     string
	Checkable    bool
}

// Anchored is a span set anchored in one document state.
type Anchored struct {
	DocumentID      string
	Epoch, Revision int64
	Spans           []AnchoredSpan
}

// Anchors mints and checks relative positions in editor documents.
type Anchors interface {
	AnchorSpans(ctx context.Context, projectID, documentID string, revision int64, spans []Span) (Anchored, error)
	// AnchorPathSpans anchors in an existing document for a path; ok is false when there is none.
	AnchorPathSpans(ctx context.Context, projectID string, target Target, spans []Span) (Anchored, bool, error)
	SpansHold(ctx context.Context, projectID, documentID string, spans []AnchoredSpan) ([]bool, error)
}

// ChatRef places a session in its chat.
type ChatRef struct {
	ProjectID string
	// SessionID is the root chat session.
	SessionID string
	// JobID is set when the resolved session is a worker child session.
	JobID string
	Title string
}

// Chats resolves any session to the chat it belongs to.
type Chats interface {
	Chat(ctx context.Context, sessionID string) (ChatRef, bool)
}

// Publisher delivers one chat's complete presence.
type Publisher interface {
	PublishAgentPresence(ctx context.Context, ev api.AgentPresenceEvent)
}
