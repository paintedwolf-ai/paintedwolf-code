package store

import (
	"context"
	"sort"
)

// SessionTreeMember contains only the identity needed to stop a session runtime.
type SessionTreeMember struct {
	ID              string
	ProjectID       string
	ParentSessionID string
}

// SessionTreeMembers returns descendants before their ancestors, without
// hydrating transcripts, workspace paths, or presentation state.
func (s *SQL) SessionTreeMembers(ctx context.Context, rootID string) ([]SessionTreeMember, error) {
	rows, err := s.queries.ListSessionTreeMembers(ctx, rootID)
	if err != nil {
		return nil, err
	}
	members := make([]SessionTreeMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, SessionTreeMember{ID: row.ID, ProjectID: row.ProjectID, ParentSessionID: row.ParentSessionID.String})
	}
	return orderSessionTree(members, rootID)
}

func (s *Memory) SessionTreeMembers(ctx context.Context, rootID string) ([]SessionTreeMember, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	members := make([]SessionTreeMember, 0)
	for _, id := range memorySessionTreeIDs(s.sessions, rootID) {
		if row := s.sessions[id]; row != nil {
			members = append(members, SessionTreeMember{ID: row.ID, ProjectID: row.ProjectID, ParentSessionID: row.ParentSessionID})
		}
	}
	return orderSessionTree(members, rootID)
}

func orderSessionTree(members []SessionTreeMember, rootID string) ([]SessionTreeMember, error) {
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	children := make(map[string][]SessionTreeMember, len(members))
	var root *SessionTreeMember
	for i, member := range members {
		children[member.ParentSessionID] = append(children[member.ParentSessionID], member)
		if member.ID == rootID {
			root = &members[i]
		}
	}
	if root == nil {
		return nil, ErrSessionNotFound
	}
	ordered := make([]SessionTreeMember, 1, len(members))
	ordered[0] = *root
	seen := map[string]bool{rootID: true}
	for i := 0; i < len(ordered); i++ {
		for _, child := range children[ordered[i].ID] {
			if !seen[child.ID] {
				seen[child.ID] = true
				ordered = append(ordered, child)
			}
		}
	}
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	return ordered, nil
}
