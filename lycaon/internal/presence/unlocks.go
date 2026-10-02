package presence

import (
	"context"
	"sync"
	"time"
)

const (
	// UnlockIdle closes an unlock when no held value has been used for this long.
	UnlockIdle = 15 * time.Minute
	// UnlockCeiling closes an unlock this long after presence opened it,
	// however busy the chat is.
	UnlockCeiling = 4 * time.Hour

	sweepInterval = 30 * time.Second
)

// EndReason says why an unlock ended.
type EndReason string

const (
	EndIdle         EndReason = "idle"
	EndCeiling      EndReason = "ceiling"
	EndScreenLocked EndReason = "screen_locked"
	EndSleep        EndReason = "sleep"
	EndAppQuit      EndReason = "app_quit"
	EndManual       EndReason = "manual"
	// EndRenewed: presence unlocked the same chat afresh.
	EndRenewed EndReason = "renewed"
	// EndRestart: the engine stopped while the unlock was open.
	EndRestart EndReason = "restart"
)

// IsLockReason reports whether reason names a lock a client may request.
func IsLockReason(reason EndReason) bool {
	switch reason {
	case EndScreenLocked, EndSleep, EndAppQuit, EndManual:
		return true
	case EndIdle, EndCeiling, EndRenewed, EndRestart:
		return false
	default:
		return false
	}
}

// Unlock is one chat's open unlock: a person verified presence, so the
// chat's approved uses of values that person holds may proceed.
type Unlock struct {
	// ID is the presence challenge that opened the unlock.
	ID            string
	ChatSessionID string
	PersonID      string
	Authenticator string
	WindowLabel   string
	UnlockedAt    time.Time
	LastUsedAt    time.Time
}

// NewUnlock is the unlock verified presence opens for chat.
func NewUnlock(chatSessionID string, verified Verified) Unlock {
	at := verified.VerifiedAt.UTC()
	return Unlock{
		ID: verified.ChallengeID, ChatSessionID: chatSessionID, PersonID: verified.PersonID,
		Authenticator: verified.Authenticator, WindowLabel: verified.WindowLabel,
		UnlockedAt: at, LastUsedAt: at,
	}
}

// ClosesAt is when the unlock ends unless a use extends it first.
func (u Unlock) ClosesAt() time.Time {
	idle := u.LastUsedAt.Add(UnlockIdle)
	if ceiling := u.ExpiresAt(); ceiling.Before(idle) {
		return ceiling
	}
	return idle
}

// ExpiresAt is the unlock's ceiling, which no use extends.
func (u Unlock) ExpiresAt() time.Time { return u.UnlockedAt.Add(UnlockCeiling) }

func (u Unlock) endedBy(now time.Time) (EndReason, bool) {
	switch {
	case !now.Before(u.ExpiresAt()):
		return EndCeiling, true
	case !now.Before(u.LastUsedAt.Add(UnlockIdle)):
		return EndIdle, true
	}
	return "", false
}

// UnlockObserver hears unlock changes outside the registry's lock.
type UnlockObserver struct {
	// Ended records an ended unlock.
	Ended func(Unlock, EndReason, time.Time)
	// Changed announces that a chat's unlock opened, extended, or ended.
	Changed func(chatSessionID string)
}

type unlockEnd struct {
	unlock Unlock
	reason EndReason
}

// Unlocks holds every chat's unlock in memory only, so an engine restart
// closes them all.
type Unlocks struct {
	mu       sync.Mutex
	now      func() time.Time
	byChat   map[string]Unlock
	observer UnlockObserver
}

// NewUnlocks returns a registry with every chat locked.
func NewUnlocks() *Unlocks {
	return &Unlocks{now: time.Now, byChat: make(map[string]Unlock)}
}

// SetClock replaces the registry's clock in tests.
func (u *Unlocks) SetClock(now func() time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.now = now
}

// SetObserver installs the audit and event hooks.
func (u *Unlocks) SetObserver(observer UnlockObserver) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.observer = observer
}

// Open starts an unlock. An unlock already open for the chat ends as renewed.
func (u *Unlocks) Open(opened Unlock) {
	u.mu.Lock()
	now := u.now().UTC()
	var ended []unlockEnd
	if previous, ok := u.byChat[opened.ChatSessionID]; ok {
		ended = append(ended, unlockEnd{previous, EndRenewed})
	}
	u.byChat[opened.ChatSessionID] = opened
	observer := u.observer
	u.mu.Unlock()
	u.notify(observer, ended, now, opened.ChatSessionID)
}

// Active returns the chat's open unlock, ending it first if it has lapsed.
func (u *Unlocks) Active(chatSessionID string) (Unlock, bool) {
	return u.current(chatSessionID, false)
}

// Use returns the chat's open unlock and extends its idle deadline.
func (u *Unlocks) Use(chatSessionID string) (Unlock, bool) {
	return u.current(chatSessionID, true)
}

func (u *Unlocks) current(chatSessionID string, use bool) (Unlock, bool) {
	if u == nil {
		return Unlock{}, false
	}
	u.mu.Lock()
	now := u.now().UTC()
	open, ok := u.byChat[chatSessionID]
	var ended []unlockEnd
	if ok {
		if reason, lapsed := open.endedBy(now); lapsed {
			delete(u.byChat, chatSessionID)
			ended = append(ended, unlockEnd{open, reason})
			ok = false
		} else if use {
			open.LastUsedAt = now
			u.byChat[chatSessionID] = open
		}
	}
	observer := u.observer
	u.mu.Unlock()
	if len(ended) > 0 || (ok && use) {
		u.notify(observer, ended, now, chatSessionID)
	}
	return open, ok
}

// Lock ends one chat's unlock.
func (u *Unlocks) Lock(chatSessionID string, reason EndReason) bool {
	if u == nil {
		return false
	}
	u.mu.Lock()
	now := u.now().UTC()
	open, ok := u.byChat[chatSessionID]
	delete(u.byChat, chatSessionID)
	observer := u.observer
	u.mu.Unlock()
	if ok {
		u.notify(observer, []unlockEnd{{open, reason}}, now, chatSessionID)
	}
	return ok
}

// LockAll ends every unlock, as when the person steps away from the device.
func (u *Unlocks) LockAll(reason EndReason) int {
	return u.close(func(Unlock, time.Time) (EndReason, bool) { return reason, true })
}

// Sweep ends every unlock that has lapsed.
func (u *Unlocks) Sweep() int {
	return u.close(func(open Unlock, now time.Time) (EndReason, bool) { return open.endedBy(now) })
}

// RunSweeper ends lapsed unlocks as they lapse, so their end is recorded
// and announced even when nothing asks for them.
func (u *Unlocks) RunSweeper(ctx context.Context) error {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			u.Sweep()
		}
	}
}

func (u *Unlocks) close(ends func(Unlock, time.Time) (EndReason, bool)) int {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	now := u.now().UTC()
	var ended []unlockEnd
	for chat, open := range u.byChat {
		if reason, closes := ends(open, now); closes {
			delete(u.byChat, chat)
			ended = append(ended, unlockEnd{open, reason})
		}
	}
	observer := u.observer
	u.mu.Unlock()
	for _, end := range ended {
		u.notify(observer, []unlockEnd{end}, now, end.unlock.ChatSessionID)
	}
	return len(ended)
}

func (u *Unlocks) notify(observer UnlockObserver, ended []unlockEnd, at time.Time, chatSessionID string) {
	for _, end := range ended {
		if observer.Ended != nil {
			observer.Ended(end.unlock, end.reason, at)
		}
	}
	if observer.Changed != nil {
		observer.Changed(chatSessionID)
	}
}
