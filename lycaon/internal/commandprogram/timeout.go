package commandprogram

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ParseDuration reads a sleep or timeout interval: decimal seconds with an
// optional s, m, h or d suffix.
func ParseDuration(word string) (time.Duration, bool) {
	unit := time.Second
	switch {
	case strings.HasSuffix(word, "s"):
		word = strings.TrimSuffix(word, "s")
	case strings.HasSuffix(word, "m"):
		word, unit = strings.TrimSuffix(word, "m"), time.Minute
	case strings.HasSuffix(word, "h"):
		word, unit = strings.TrimSuffix(word, "h"), time.Hour
	case strings.HasSuffix(word, "d"):
		word, unit = strings.TrimSuffix(word, "d"), 24*time.Hour
	}
	value, err := strconv.ParseFloat(word, 64)
	if err != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) || strings.ContainsAny(word, "eEnN+") {
		return 0, false
	}
	scaled := value * float64(unit)
	if scaled >= float64(math.MaxInt64) {
		return 0, false
	}
	return time.Duration(scaled), true
}

// TimeoutPlan is one bound timeout invocation. A zero Duration or KillAfter
// disables that timer.
type TimeoutPlan struct {
	Duration, KillAfter time.Duration
	Signal              Signal
	PreserveStatus      bool
}

// TimedOutStatus reports an expired command.
const TimedOutStatus = 124

var errTimedOut = errors.New("timeout expired")

// RunTimeout supervises one inner command. On expiry the plan's signal goes
// to the process the inner command attached to the relay; an inner command
// with no process (a native operation) is ended through its context. With
// KillAfter, KILL follows the same way. sent observes each delivery. The
// caller's context is the outer bound and always wins: its cancellation
// returns the inner outcome unchanged.
func RunTimeout(ctx context.Context, clock Clock, plan TimeoutPlan, sent func(Signal), run func(context.Context, *SignalRelay) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	inner, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	relay := &SignalRelay{}
	var (
		mu       sync.Mutex
		finished bool
		expired  bool
		last     Signal
		stops    []func() bool
	)
	schedule := func(d time.Duration, f func()) {
		if d <= 0 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			stops = append(stops, clock.AfterFunc(d, f))
		}
	}
	send := func(sig Signal) {
		mu.Lock()
		if finished {
			mu.Unlock()
			return
		}
		expired, last = true, sig
		mu.Unlock()
		if sent != nil {
			sent(sig)
		}
		if !relay.Deliver(sig) {
			cancel(errTimedOut)
		}
	}
	schedule(plan.Duration, func() {
		send(plan.Signal)
		schedule(plan.KillAfter, func() { send(Kill) })
	})
	result, err := run(inner, relay)
	mu.Lock()
	finished = true
	for _, stop := range stops {
		stop()
	}
	fired, sig := expired, last
	mu.Unlock()
	if !fired || ctx.Err() != nil {
		return result, err
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return result, err
	}
	status := TimedOutStatus
	if plan.PreserveStatus {
		status = result.Status
		if result.Terminal == toolcontract.ProducerInterrupted {
			// A native operation ended by its context reports the signal it stood for.
			status = 128 + sig.Number
		}
	}
	certainty := result.EffectCertainty
	if result.Validate() != nil {
		certainty = toolcontract.EffectUnknown
	}
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: certainty}, nil
}
