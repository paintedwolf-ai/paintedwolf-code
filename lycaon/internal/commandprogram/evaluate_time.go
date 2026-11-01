package commandprogram

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

// runTimed runs the pipeline after time and reports its real, user and system
// time on the frame's stderr, outside the pipeline's own redirects. Real
// time is the clock's; user and system time are what external children
// reported when reaped. Native operations have no processor time of their
// own in the host process, so they count zero and the outcome says so.
func runTimed(ctx context.Context, f *frame, s commandsurface.Statement, c commandsurface.TimedCommand) (signal, error) {
	clock := f.e.clock()
	account := &ProcessTimes{enclosing: f.times}
	start := clock.Now()
	var sig signal
	var err error
	if len(c.Pipeline) > 0 {
		inner := *f
		inner.times = account
		sig, err = inner.statements(ctx, c.Pipeline)
	} else {
		err = f.builtinStatus(s.SyntaxLocation, "time", 0)
	}
	if ctx.Err() != nil {
		return sig, err
	}
	user, sys, unmeasured := account.totals()
	if unmeasured {
		f.e.cpuUnmeasured.Store(true)
	}
	if _, writeErr := io.WriteString(f.io.streams.Error, FormatTimes(clock.Now().Sub(start), user, sys, c.POSIX)); writeErr != nil && err == nil {
		err = writeErr
	}
	return sig, err
}

// FormatTimes renders a time report as bash does: by default a blank line
// and tab-separated minutes and seconds to three places, with -p the POSIX
// form in seconds to two places. Fractions truncate.
func FormatTimes(real, user, sys time.Duration, posix bool) string {
	if posix {
		return fmt.Sprintf("real %s\nuser %s\nsys %s\n", seconds(real, 2), seconds(user, 2), seconds(sys, 2))
	}
	return fmt.Sprintf("\nreal\t%s\nuser\t%s\nsys\t%s\n", minutes(real), minutes(user), minutes(sys))
}

func seconds(d time.Duration, places int) string {
	scale := time.Duration(1)
	for range 9 - places {
		scale *= 10
	}
	d = max(d, 0).Truncate(scale)
	whole := d / time.Second
	frac := (d % time.Second) / scale
	return fmt.Sprintf("%d.%0*d", whole, places, frac)
}

func minutes(d time.Duration) string {
	d = max(d, 0)
	return fmt.Sprintf("%dm%ss", d/time.Minute, seconds(d%time.Minute, 3))
}

func (e *evaluation) clock() Clock {
	if e.ports.Clock != nil {
		return e.ports.Clock
	}
	return SystemClock()
}
