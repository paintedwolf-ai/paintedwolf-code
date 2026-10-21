package lineage

import (
	"context"
	"fmt"
)

// OwnedSockets observes TCP resources held by a current inherited-descriptor
// member. It performs fresh descriptor and process-incarnation checks; neither
// a cached numeric PID nor a requested listener establishes this relationship.
// The result is a momentary observation, not permission for a later dial/write.
func OwnedSockets(ctx context.Context, id ID) ([]SocketObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registry.mu.Lock()
	l := registry.live[id]
	if l == nil {
		for _, candidate := range registry.retired {
			if candidate.id == id {
				l = candidate
				break
			}
		}
	}
	registry.mu.Unlock()
	if l == nil {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.read == nil {
		return nil, nil
	}
	holders, err := pipeHolders([]uint64{l.peer})
	if err != nil {
		return nil, err
	}
	var result []SocketObservation
	for _, pid := range holders[0] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		identity, ok := processIdentity(pid)
		if !ok {
			continue
		}
		sockets, err := SocketsOf(pid)
		if err != nil {
			return nil, fmt.Errorf("observe lineage socket holder: %w", err)
		}
		// A second inherited-descriptor walk brackets socket observation. A holder
		// that closed its lineage or changed incarnation cannot carry this result.
		current, err := pipeHolders([]uint64{l.peer})
		if err != nil {
			return nil, err
		}
		held := false
		for _, candidate := range current[0] {
			if candidate == pid {
				held = true
				break
			}
		}
		if !held || !identity.Current() {
			continue
		}
		for _, socket := range sockets {
			if socket.Holder == identity {
				result = append(result, socket)
			}
		}
	}
	return result, nil
}
