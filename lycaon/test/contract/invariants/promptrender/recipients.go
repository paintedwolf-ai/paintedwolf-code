package promptrender

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// The frozen coordinator and worker authorities are the largest working set
// the prompt-render checks share. A run that reads them holds them: the
// host-state matrix, the condition ledger, a skill audit and an overlay reach
// sweep each hold them for their whole run, and nested holds share one set.
// The last release drops the set, hoststate's freeze memos and the claim
// memos keyed by the authorities' digests; a later hold freezes afresh.
var recipients struct {
	mu      sync.Mutex
	holds   int
	set     *recipientSet
	release func()
}

type recipientSet struct {
	coordinatorPairs func() ([]coordRep, error)
	coordinatorReps  func() ([]coordRep, error)
	workerPairs      func() ([]workerRep, error)
	workerReps       func() ([]workerRep, error)
}

var errRecipientsNotHeld = errors.New("promptrender: recipient authorities read outside a hold")

// holdRecipients opens a hold on the recipient authorities until the
// returned release runs.
func holdRecipients() (release func()) {
	recipients.mu.Lock()
	defer recipients.mu.Unlock()
	if recipients.holds == 0 {
		recipients.set = newRecipientSet()
		recipients.release = hoststate.HoldAuthorityMemos()
	}
	recipients.holds++
	return sync.OnceFunc(func() {
		recipients.mu.Lock()
		defer recipients.mu.Unlock()
		recipients.holds--
		if recipients.holds > 0 {
			return
		}
		recipients.set = nil
		recipients.release()
		recipients.release = nil
		claimEvalMemo.Clear()
		claimAuthKeyMemo.Clear()
		workerOccByReaderFP.Clear()
	})
}

func heldRecipients() (*recipientSet, error) {
	recipients.mu.Lock()
	defer recipients.mu.Unlock()
	if recipients.set == nil {
		return nil, errRecipientsNotHeld
	}
	return recipients.set, nil
}

func newRecipientSet() *recipientSet {
	set := &recipientSet{}
	set.coordinatorPairs = sync.OnceValues(freezeCoordinatorPairs)
	// The digest omits reader facts such as root count, so a recipient is
	// distinct by authority and reader.
	set.coordinatorReps = sync.OnceValues(func() ([]coordRep, error) {
		pairs, err := set.coordinatorPairs()
		return distinctRecipients(pairs, func(p coordRep) hoststate.TurnAuthority { return p.auth }), err
	})
	set.workerPairs = sync.OnceValues(freezeWorkerPairs)
	set.workerReps = sync.OnceValues(func() ([]workerRep, error) {
		pairs, err := set.workerPairs()
		return distinctRecipients(pairs, func(p workerRep) hoststate.TurnAuthority { return p.auth }), err
	})
	return set
}

func freezeCoordinatorPairs() ([]coordRep, error) {
	ctx := context.Background()
	states, err := hoststate.CoordinatorStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]coordRep, 0, len(states))
	for _, st := range states {
		if st.Excluded {
			continue
		}
		auth, err := hoststate.FreezeTurnAuthority(ctx, st)
		if err != nil {
			return nil, err
		}
		out = append(out, coordRep{state: st, auth: auth})
	}
	return out, nil
}

func freezeWorkerPairs() ([]workerRep, error) {
	ctx := context.Background()
	workerStates, err := hoststate.WorkerStates(ctx)
	if err != nil {
		return nil, err
	}
	projectAgents, err := projectAgentWorkerStates()
	if err != nil {
		return nil, err
	}
	workerStates = slices.Concat(workerStates, projectAgents)
	out := make([]workerRep, 0, len(workerStates))
	for _, ws := range workerStates {
		if ws.Excluded {
			continue
		}
		auth, err := hoststate.FreezeWorkerAuthority(ctx, ws)
		if err != nil {
			return nil, err
		}
		out = append(out, workerRep{ws: ws, auth: auth})
	}
	return out, nil
}

func distinctRecipients[T any](pairs []T, auth func(T) hoststate.TurnAuthority) []T {
	seen := map[string]bool{}
	var out []T
	for _, p := range pairs {
		a := auth(p)
		if key := a.Digest + "|" + a.Reader.Fingerprint(); !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}

// cachedCoordinatorPairs is every admitted coordinator state with its frozen
// authority, from the open hold.
func cachedCoordinatorPairs() ([]coordRep, error) {
	set, err := heldRecipients()
	if err != nil {
		return nil, err
	}
	return set.coordinatorPairs()
}

// cachedCoordinatorReps is one coordinator state per distinct authority and
// reader, from the open hold.
func cachedCoordinatorReps() ([]coordRep, error) {
	set, err := heldRecipients()
	if err != nil {
		return nil, err
	}
	return set.coordinatorReps()
}

// cachedWorkerPairs is every admitted worker state with its frozen
// authority, from the open hold.
func cachedWorkerPairs() ([]workerRep, error) {
	set, err := heldRecipients()
	if err != nil {
		return nil, err
	}
	return set.workerPairs()
}

// cachedWorkerReps is one worker state per distinct authority and reader,
// from the open hold.
func cachedWorkerReps() ([]workerRep, error) {
	set, err := heldRecipients()
	if err != nil {
		return nil, err
	}
	return set.workerReps()
}
