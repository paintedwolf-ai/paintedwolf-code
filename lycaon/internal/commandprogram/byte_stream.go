package commandprogram

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// StreamTermination describes the byte plane, independently of utility status
// or an owner's effect outcome. EOF alone never authorizes publication.
type StreamTermination string

const (
	StreamOpen            StreamTermination = "open"
	StreamEOF             StreamTermination = "eof"
	StreamConsumerClosed  StreamTermination = "consumer_closed"
	StreamCanceled        StreamTermination = "canceled"
	StreamIncomplete      StreamTermination = "incomplete"
	StreamBudgetExceeded  StreamTermination = "budget_exceeded"
	StreamScreeningFailed StreamTermination = "screening_failed"
)

// StreamCompletion is side metadata. Neither previews nor source mappings are
// inserted into downstream bytes. Arbitrary transforms drop mapping eligibility.
type StreamCompletion struct {
	Termination    StreamTermination
	InputBytes     int64
	EmittedBytes   int64
	ReceivedSHA256 string
	EmittedSHA256  string
	Redacted       bool
	Mappings       []toolcontract.StreamSourceMapping
}

// StreamLimitError reports attempted ingress before its bytes enter the pipe.
type StreamLimitError struct {
	Limit             string
	Maximum, Observed int64
	Overflow          bool
}

func (e *StreamLimitError) Error() string {
	return fmt.Sprintf("stream %s ceiling exceeded: %d > %d", e.Limit, e.Observed, e.Maximum)
}

// ByteBudget meters ingress/spill independently of any preview. A program may
// share one budget across streams; it is not a grant or an operation owner.
type ByteBudget struct {
	mu      sync.Mutex
	maximum int64
	used    int64
	limit   string
}

func NewByteBudget(limit string, maximum int64) (*ByteBudget, error) {
	if maximum <= 0 || limit == "" {
		return nil, fmt.Errorf("stream budget requires a named positive ceiling")
	}
	return &ByteBudget{maximum: maximum, limit: limit}, nil
}
func (b *ByteBudget) Charge(n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < 0 {
		return fmt.Errorf("stream charge must not be negative")
	}
	if n > b.maximum-b.used {
		overflow := n > int64(^uint64(0)>>1)-b.used
		observed := int64(^uint64(0) >> 1)
		if !overflow {
			observed = b.used + n
		}
		return &StreamLimitError{Limit: b.limit, Maximum: b.maximum, Observed: observed, Overflow: overflow}
	}
	b.used += n
	return nil
}
func (b *ByteBudget) Used() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.used }
