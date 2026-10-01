package editordoc

import (
	"fmt"
	"strings"

	"context"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *Service) publicationTextState(ctx context.Context, d *Document, checkpoint []byte) (*sourceledger.TextState, error) {
	if len(checkpoint) == 0 {
		return nil, nil
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return nil, err
	}
	s.replicas.next++
	handle := s.replicas.next
	defer s.replicas.drop(context.WithoutCancel(ctx), handle)
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "open", Handle: handle, Client: entry.head.HostClient,
		Update: checkpoint, Authorship: true, OmitText: true})
	if err != nil {
		return nil, err
	}
	state := &sourceledger.TextState{DocumentID: d.ID, Epoch: entry.head.Epoch, Spans: make([]sourceledger.TextSpan, len(snapshot.Authors))}
	for i, span := range snapshot.Authors {
		state.Spans[i] = sourceledger.TextSpan{Index: span.Index, Length: span.Length, Client: span.Client, Clock: span.Clock}
	}
	return state, nil
}

type authorshipContext struct {
	sessionID, jobID, toolCallID, toolName string
	turn                                   int
}

// Text actor kinds mirror editor_replica_updates.actor_kind.
const (
	actorUser     = "user"
	actorAgent    = "agent"
	actorExternal = "external"
	actorRestore  = "restore"
)

// textActor records the person and client responsible for a text transition.
type textActor struct {
	kind     string
	personID string
	clientID string
}

// hostReplica is the host replica identity a non-agent transition edits through.
func (a textActor) hostReplica() string {
	if a.clientID != "" {
		return a.clientID
	}
	return "filesystem"
}

var filesystemActor = textActor{kind: actorExternal}

// personActor attributes a client's transition to the acting person.
func (s *Service) personActor(ctx context.Context, kind, clientID string) (textActor, error) {
	person, err := people.Acting(ctx, s.store.people)
	if err != nil {
		return textActor{}, fmt.Errorf("editor actor: %w", err)
	}
	return textActor{kind: kind, personID: person.ID, clientID: strings.TrimSpace(clientID)}, nil
}

func textContribution(d *Document, snapshot *documentcore.Snapshot, actor textActor, operationID string) *sourceledger.TextContribution {
	if len(snapshot.Inserted) == 0 && len(snapshot.Deleted) == 0 {
		return nil
	}
	origin := api.SourceChangeOriginUser
	switch actor.kind {
	case actorAgent:
		origin = api.SourceChangeOriginAgent
	case actorExternal:
		origin = api.SourceChangeOriginExternal
	}
	return &sourceledger.TextContribution{
		ProjectID: d.ProjectID, FileID: d.FileID, DocumentID: d.ID, Epoch: d.Epoch, Revision: d.Revision,
		OperationID: operationID, PersonID: actor.personID, ClientID: actor.clientID, Origin: origin, SessionID: d.authorship.sessionID,
		Turn: d.authorship.turn, ToolCallID: d.authorship.toolCallID, ToolName: d.authorship.toolName, JobID: d.authorship.jobID,
		Inserted: contributionRanges(snapshot.Inserted), Deleted: contributionRanges(snapshot.Deleted), CreatedAt: d.UpdatedAt,
	}
}

func contributionRanges(ranges []documentcore.IdentityRange) []sourceledger.TextIdentityRange {
	result := make([]sourceledger.TextIdentityRange, len(ranges))
	for i, span := range ranges {
		result[i] = sourceledger.TextIdentityRange{Client: span.Client, Start: span.Start, End: span.End}
	}
	return result
}

// Host participants always edit the current head, so their clocks can be reused
// across operations. The contribution ledger retains each operation's ranges.
func (s *Service) hostTextParticipant(ctx context.Context, d *Document, actor textActor) (uint32, error) {
	if actor.kind == actorAgent {
		return s.registerReplica(ctx, d.ID, "agent", d.authorship.sessionID, d.authorship.sessionID, "")
	}
	replica := actor.hostReplica()
	return s.registerReplica(ctx, d.ID, "host", replica, actor.kind+":"+replica, "")
}
