package editordoc

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
)

const participantTTL = time.Minute

type PresenceRange struct {
	Anchor []byte
	Head   []byte
}

type Participant struct {
	expiry   *time.Timer
	ClientID string
	// PersonID is the person working in the client.
	PersonID     string
	Incarnation  string
	WindowNumber int
	Ranges       []PresenceRange
	Main         int
	UpdatedAt    time.Time
}

func (s *Service) joinParticipant(ctx context.Context, documentID, clientID, incarnation string) bool {
	if strings.TrimSpace(clientID) == "" {
		return false
	}
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	if s.participants[documentID] == nil {
		s.participants[documentID] = make(map[string]Participant)
	}
	participant, existed := s.participants[documentID][clientID]
	if incarnation != "" && participant.Incarnation != incarnation {
		participant.Ranges, participant.Main = nil, 0
		participant.Incarnation = incarnation
		existed = false
	}
	participant.ClientID, participant.UpdatedAt = clientID, time.Now().UTC()
	if s.windowNumbers[clientID] == 0 {
		s.windowNumbers[clientID] = len(s.windowNumbers) + 1
	}
	participant.WindowNumber = s.windowNumbers[clientID]
	participant.PersonID = s.clientPeople[clientID]
	s.refreshParticipant(ctx, documentID, &participant)
	s.participants[documentID][clientID] = participant
	return !existed
}

// A client ID remains bound to one person.
func (s *Service) bindClientPerson(clientID, personID string) error {
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	if bound, ok := s.clientPeople[clientID]; ok && bound != personID {
		return ErrReplicaIdentity
	}
	s.clientPeople[clientID] = personID
	return nil
}

type ReplicaJoin struct {
	ClientID    string
	Incarnation string
	Epoch       int64
	Vector      []byte
}

func (s *Service) Join(ctx context.Context, id, projectID string, in ReplicaJoin) (*Document, error) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	unlock := s.docLocks.lock(documentLockKey(id))
	defer unlock()
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return nil, err
	}
	p, err := s.projectBranch(ctx, projectID, d.BranchID)
	if err != nil {
		return nil, err
	}
	if err := s.reconcileDocumentDisk(ctx, p, d, ""); err != nil {
		return nil, err
	}
	clientID, incarnation, vector := in.ClientID, in.Incarnation, in.Vector
	if strings.TrimSpace(clientID) == "" || incarnation == "" || len(clientID) > 256 || len(incarnation) > 256 {
		return nil, ErrReplicaIdentity
	}
	head, err := s.store.replicaHead(ctx, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if head == nil || in.Epoch != head.Epoch {
		vector = nil
	}
	if err := s.replicaProjection(ctx, d, vector); err != nil {
		return nil, err
	}
	actor, err := s.personActor(ctx, actorUser, clientID)
	if err != nil {
		return nil, err
	}
	if err := s.bindClientPerson(clientID, actor.personID); err != nil {
		return nil, err
	}
	replica, err := s.registerReplica(ctx, id, "person", clientID, incarnation, actor.personID)
	if err != nil {
		return nil, err
	}
	d.ReplicaID = replica
	if s.joinParticipant(ctx, id, clientID, incarnation) {
		s.changed(ctx, d, false)
	}
	return s.withParticipants(d), nil
}

// A replica incarnation remains bound to its client and person.
func (s *Service) registerReplica(ctx context.Context, id, role, clientID, incarnation, personID string) (uint32, error) {
	var replica uint32
	var registered, registeredPerson string
	err := s.store.db.QueryRowContext(ctx, `SELECT replica_id,client_id,COALESCE(person_id,'') FROM editor_replicas WHERE document_id=? AND role=? AND incarnation=?`, id, role, incarnation).Scan(&replica, &registered, &registeredPerson)
	if err == nil {
		if registered != clientID || registeredPerson != personID {
			return 0, ErrReplicaIdentity
		}
		return replica, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	head, err := s.store.replicaHead(ctx, id)
	if err != nil {
		return 0, err
	}
	clocks, err := documentcore.VectorClocks(head.Vector)
	if err != nil {
		return 0, err
	}
	for {
		replica, err = randomReplicaID()
		if err != nil {
			return 0, err
		}
		if clocks[replica] != 0 {
			continue
		}
		result, err := s.store.db.ExecContext(ctx, `INSERT INTO editor_replicas (document_id,role,client_id,person_id,incarnation,replica_id,created_at)
			SELECT ?,?,?,?,?,?,? WHERE ?<>(SELECT host_client FROM editor_replica_heads WHERE document_id=?) AND ?<>(SELECT filesystem_client FROM editor_replica_heads WHERE document_id=?)
			ON CONFLICT(document_id,replica_id) DO NOTHING`, id, role, clientID, nullableString(personID), incarnation, replica, time.Now().UTC().Format(time.RFC3339Nano), replica, id, replica, id)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 1 {
			return replica, nil
		}
	}
}

func (s *Service) Leave(ctx context.Context, id, projectID, clientID, incarnation string) error {
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return err
	}
	actor, err := s.personActor(ctx, actorUser, clientID)
	if err != nil {
		return err
	}
	s.presenceMu.Lock()
	// An open that never joined has no incarnation to match; leaving clears it too.
	if current, exists := s.participants[id][clientID]; !exists || (current.Incarnation != "" && current.Incarnation != incarnation) || current.PersonID != actor.personID {
		s.presenceMu.Unlock()
		return nil
	}
	if participant := s.participants[id][clientID]; participant.expiry != nil {
		participant.expiry.Stop()
	}
	delete(s.participants[id], clientID)
	if len(s.participants[id]) == 0 {
		delete(s.participants, id)
	}
	s.presenceMu.Unlock()
	s.changed(ctx, d, false)
	return nil
}

// DisconnectClient releases what a client holds only while live: presence and
// its person binding. Documents, replicas, and retention stay.
func (s *Service) DisconnectClient(ctx context.Context, clientID string) {
	s.presenceMu.Lock()
	var ids []string
	for id, participants := range s.participants {
		if _, ok := participants[clientID]; ok {
			ids = append(ids, id)
			if participant := participants[clientID]; participant.expiry != nil {
				participant.expiry.Stop()
			}
			delete(participants, clientID)
		}
		if len(participants) == 0 {
			delete(s.participants, id)
		}
	}
	delete(s.clientPeople, clientID)
	s.presenceMu.Unlock()
	for _, id := range ids {
		if d, err := s.store.Get(ctx, id); err == nil {
			s.changed(ctx, d, false)
		}
	}
}

func (s *Service) UpdatePresence(ctx context.Context, id, projectID string, participant Participant) error {
	d, err := s.checked(ctx, id, projectID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(participant.ClientID) == "" || len(participant.ClientID) > 256 || len(participant.Ranges) > 256 || participant.Main < 0 || (len(participant.Ranges) > 0 && participant.Main >= len(participant.Ranges)) || (len(participant.Ranges) == 0 && participant.Main != 0) {
		return ErrReplicaIdentity
	}
	for _, selected := range participant.Ranges {
		if len(selected.Anchor) == 0 || len(selected.Head) == 0 || len(selected.Anchor) > 1024 || len(selected.Head) > 1024 {
			return ErrReplicaIdentity
		}
	}
	actor, err := s.personActor(ctx, actorUser, participant.ClientID)
	if err != nil {
		return err
	}
	s.presenceMu.Lock()
	previous, joined := s.participants[id][participant.ClientID]
	if !joined || s.presenceClosed || participant.Incarnation == "" || previous.Incarnation != participant.Incarnation || previous.PersonID != actor.personID {
		s.presenceMu.Unlock()
		return ErrReplicaIdentity
	}
	changed := previous.Main != participant.Main || !slices.EqualFunc(previous.Ranges, participant.Ranges, func(a, b PresenceRange) bool { return bytes.Equal(a.Anchor, b.Anchor) && bytes.Equal(a.Head, b.Head) })
	participant.WindowNumber, participant.PersonID = previous.WindowNumber, previous.PersonID
	participant.Ranges = clonePresenceRanges(participant.Ranges)
	participant.expiry = previous.expiry
	participant.UpdatedAt = time.Now().UTC()
	s.refreshParticipant(ctx, id, &participant)
	s.participants[id][participant.ClientID] = participant
	s.presenceMu.Unlock()
	if changed {
		s.changed(ctx, d, false)
	}
	return nil
}

// Caller holds presenceMu. A heartbeat extends presence without creating a
// content event or keeping durable work alive.
func (s *Service) refreshParticipant(ctx context.Context, id string, participant *Participant) {
	if participant.expiry != nil {
		participant.expiry.Stop()
	}
	clientID, updated := participant.ClientID, participant.UpdatedAt
	participant.expiry = time.AfterFunc(participantTTL, func() { s.expireParticipant(context.WithoutCancel(ctx), id, clientID, updated) })
}

func (s *Service) expireParticipant(ctx context.Context, id, clientID string, updated time.Time) {
	s.ops.RLock()
	defer s.ops.RUnlock()
	s.presenceMu.Lock()
	participant, exists := s.participants[id][clientID]
	if s.presenceClosed || !exists || !participant.UpdatedAt.Equal(updated) {
		s.presenceMu.Unlock()
		return
	}
	delete(s.participants[id], clientID)
	if len(s.participants[id]) == 0 {
		delete(s.participants, id)
	}
	s.presenceMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if d, err := s.store.Get(ctx, id); err == nil {
		s.changed(ctx, d, false)
	}
}

func (s *Service) closePresence() {
	s.presenceMu.Lock()
	defer s.presenceMu.Unlock()
	s.presenceClosed = true
	for _, participants := range s.participants {
		for _, participant := range participants {
			if participant.expiry != nil {
				participant.expiry.Stop()
			}
		}
	}
	clear(s.participants)
}

func (s *Service) withParticipants(d *Document) *Document {
	out := *d
	out.Participants = nil
	s.presenceMu.Lock()
	for _, participant := range s.participants[d.ID] {
		participant.Ranges = clonePresenceRanges(participant.Ranges)
		out.Participants = append(out.Participants, participant)
	}
	s.presenceMu.Unlock()
	sort.Slice(out.Participants, func(i, j int) bool { return out.Participants[i].ClientID < out.Participants[j].ClientID })
	return &out
}

func clonePresenceRanges(ranges []PresenceRange) []PresenceRange {
	out := make([]PresenceRange, len(ranges))
	for i, selected := range ranges {
		out[i] = PresenceRange{Anchor: bytes.Clone(selected.Anchor), Head: bytes.Clone(selected.Head)}
	}
	return out
}
