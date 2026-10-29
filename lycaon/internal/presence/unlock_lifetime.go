package presence

import (
	_ "embed"
	"encoding/json"
	"time"
)

// unlockLifetimeJSON is the one definition of an unlock's lifetime, which the
// Linux desktop shell also embeds: where it cannot observe the screen lock,
// input idle for the unlock idle period is how it sees the person step away.
//
//go:embed unlock-lifetime.json
var unlockLifetimeJSON []byte

var unlockLifetime = func() struct{ idle, ceiling time.Duration } {
	var raw struct {
		IdleSeconds    int64 `json:"idle_seconds"`
		CeilingSeconds int64 `json:"ceiling_seconds"`
	}
	if err := json.Unmarshal(unlockLifetimeJSON, &raw); err != nil || raw.IdleSeconds <= 0 || raw.CeilingSeconds <= raw.IdleSeconds {
		panic("presence: invalid embedded unlock lifetime")
	}
	return struct{ idle, ceiling time.Duration }{
		idle:    time.Duration(raw.IdleSeconds) * time.Second,
		ceiling: time.Duration(raw.CeilingSeconds) * time.Second,
	}
}()

var (
	// UnlockIdle closes an unlock when no held value has been used for this long.
	UnlockIdle = unlockLifetime.idle
	// UnlockCeiling closes an unlock this long after presence opened it,
	// however busy the chat is.
	UnlockCeiling = unlockLifetime.ceiling
)
