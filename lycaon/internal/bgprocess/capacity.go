package bgprocess

import "sort"

// BackgroundCapacityError retains the admission snapshot, before another process
// can exit or enter the session.
type BackgroundCapacityError struct {
	Limit          int
	TerminalIDs    []string
	CommandHandles []string
}

func (e *BackgroundCapacityError) Error() string { return ErrBackgroundCapReached.Error() }
func (e *BackgroundCapacityError) Unwrap() error { return ErrBackgroundCapReached }

func (r *processTable) backgroundCapacityErrorLocked(sessionID string) *BackgroundCapacityError {
	err := &BackgroundCapacityError{Limit: r.maxBackground}
	for _, proc := range r.sessions[sessionID] {
		if proc.mode != JobModeBackground || !proc.running {
			continue
		}
		if proc.kind == processKindPTY {
			err.TerminalIDs = append(err.TerminalIDs, proc.Handle)
		} else {
			err.CommandHandles = append(err.CommandHandles, proc.Handle)
		}
	}
	sort.Strings(err.TerminalIDs)
	sort.Strings(err.CommandHandles)
	return err
}

// AwaitedCapacityError freezes the configured bound and owned jobs at admission.
type AwaitedCapacityError struct {
	Limit   int
	Handles []string
}

func (e *AwaitedCapacityError) Error() string { return ErrAwaitedCapReached.Error() }
func (e *AwaitedCapacityError) Unwrap() error { return ErrAwaitedCapReached }
func (r *processTable) awaitedCapacityErrorLocked(sessionID string) *AwaitedCapacityError {
	e := &AwaitedCapacityError{Limit: r.maxAwaited}
	for _, proc := range r.sessions[sessionID] {
		if proc.mode == JobModeAwaited && proc.running {
			e.Handles = append(e.Handles, proc.Handle)
		}
	}
	sort.Strings(e.Handles)
	return e
}
