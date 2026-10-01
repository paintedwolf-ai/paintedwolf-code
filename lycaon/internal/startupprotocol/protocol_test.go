package startupprotocol

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type notifyingBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	writes chan struct{}
}

func (b *notifyingBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buffer.Write(payload)
	select {
	case b.writes <- struct{}{}:
	default:
	}
	return n, err
}

func (b *notifyingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func decodeEvents(t *testing.T, raw string) []event {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	var events []event
	for {
		var record event
		err := dec.Decode(&record)
		if err == io.EOF {
			return events
		}
		testutil.FailErr(t, "decode startup event", err)
		events = append(events, record)
	}
}

func TestReporterPublishesPhasesHeartbeatsAndReady(t *testing.T) {
	protocol := notifyingBuffer{writes: make(chan struct{}, 8)}
	var trace bytes.Buffer
	reporter, err := newReporter(&protocol, &trace, time.Millisecond)
	testutil.FailErr(t, "create startup reporter", err)

	testutil.FailErr(t, "publish provider phase", reporter.Phase(PhaseProviders))
	for range 3 {
		select {
		case <-protocol.writes:
		case <-time.After(time.Second):
			t.Fatal("startup heartbeat was not published")
		}
	}
	testutil.FailErr(t, "publish ready", reporter.Ready(43123))
	reporter.Close()

	events := decodeEvents(t, protocol.String())
	if len(events) < 4 {
		t.Fatalf("events = %d, want launch + phase + heartbeat + ready: %s", len(events), protocol.String())
	}
	if events[0].Kind != kindPhase || events[0].Phase != PhaseLaunch {
		t.Fatalf("first event = %+v, want launch", events[0])
	}
	last := events[len(events)-1]
	if last.Kind != kindReady || last.Phase != PhaseReady || last.Port != 43123 {
		t.Fatalf("last event = %+v, want ready on 43123", last)
	}
	for i, event := range events {
		if event.Protocol != version || event.Sequence != uint64(i+1) || event.PID <= 0 {
			t.Fatalf("event[%d] envelope = %+v", i, event)
		}
	}
	if strings.Contains(trace.String(), "heartbeat") {
		t.Fatalf("heartbeat flooded trace: %s", trace.String())
	}
}

func TestReporterRejectsUnknownAndTerminalProgressPhases(t *testing.T) {
	var protocol bytes.Buffer
	reporter, err := newReporter(&protocol, nil, time.Hour)
	testutil.FailErr(t, "create startup reporter", err)
	defer reporter.Close()

	if err := reporter.Phase(Phase("invented")); err == nil {
		t.Fatal("unknown startup phase accepted")
	}
	if err := reporter.Phase(PhaseReady); err == nil {
		t.Fatal("ready accepted as a progress phase")
	}
}

func TestReporterFailureKeepsCurrentPhase(t *testing.T) {
	var protocol bytes.Buffer
	reporter, err := newReporter(&protocol, nil, time.Hour)
	testutil.FailErr(t, "create startup reporter", err)
	testutil.FailErr(t, "publish pricing phase", reporter.Phase(PhasePricing))
	testutil.FailErr(t, "publish failure", reporter.Failed("build_failed"))
	reporter.Close()

	events := decodeEvents(t, protocol.String())
	last := events[len(events)-1]
	if last.Kind != kindFailed || last.Phase != PhasePricing || last.Code != "build_failed" {
		t.Fatalf("failure = %+v", last)
	}
}

func TestReporterRejectsWritesAfterTerminalEvent(t *testing.T) {
	var protocol bytes.Buffer
	reporter, err := newReporter(&protocol, nil, time.Hour)
	testutil.FailErr(t, "create startup reporter", err)
	testutil.FailErr(t, "publish ready", reporter.Ready(43123))
	before := protocol.String()
	if err := reporter.Phase(PhaseStore); err == nil {
		t.Fatal("terminal reporter accepted another phase")
	}
	reporter.Close()
	if protocol.String() != before {
		t.Fatalf("terminal reporter wrote another event: %s", protocol.String())
	}
}

func TestReporterRequiresFailureCode(t *testing.T) {
	var protocol bytes.Buffer
	reporter, err := newReporter(&protocol, nil, time.Hour)
	testutil.FailErr(t, "create startup reporter", err)
	defer reporter.Close()

	if err := reporter.Failed(""); err == nil {
		t.Fatal("empty startup failure code accepted")
	}
}
