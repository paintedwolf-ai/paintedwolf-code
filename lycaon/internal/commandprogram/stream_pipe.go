package commandprogram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ErrConsumerClosed distinguishes a deliberate downstream stop from producer
// failure. The evaluator still settles the producer independently of this pipe.
var ErrConsumerClosed error = consumerClosedError{}

// consumerClosureWitness is sealed to the byte endpoint owner. Only an
// immediate error carrying this exact pipe may justify early completion.
type consumerClosureWitness interface {
	consumerClosedEndpoint() *consumerEndpointIdentity
}

// Nonzero size guarantees distinct endpoint identities without retaining a pipe.
type consumerEndpointIdentity struct{ marker byte }

type consumerClosedError struct{ endpoint *consumerEndpointIdentity }

func (consumerClosedError) Error() string                                       { return "command stream consumer closed" }
func (consumerClosedError) ConsumerClosed() bool                                { return true }
func (e consumerClosedError) consumerClosedEndpoint() *consumerEndpointIdentity { return e.endpoint }

// The sentinel supports diagnostic classification; it carries no endpoint
// identity and cannot authorize pipeline publication.
func (consumerClosedError) Is(target error) bool {
	witness, ok := target.(consumerClosureWitness)
	return ok && witness.consumerClosedEndpoint() == nil
}

// BytePipe carries exact producer bytes without a display projection or an
// intermediate buffer. Writes return only after consumption or stream closure.
type BytePipe struct {
	identity      *consumerEndpointIdentity
	stream        *pipeRendezvous
	budget        *ByteBudget
	done          chan struct{}
	once          sync.Once
	mu            sync.Mutex
	digest        hash.Hash
	completion    StreamCompletion
	terminalError error
	mapping       pipeMappings
}

// pipeMappings holds the producer's source mappings in stream coordinates.
// They are final once the producer settles; nil memory keeps none.
type pipeMappings struct {
	ctx      context.Context
	memory   *toolcontract.ProgramMemory
	records  []toolcontract.StreamSourceMapping
	releases []func()
	disabled bool
	settled  chan struct{}
	once     sync.Once
}

// NewBytePipe connects one producer to one consumer. With memory, the pipe
// also keeps the producer's source mappings for MappedInput consumers.
func NewBytePipe(ctx context.Context, budget *ByteBudget, memory *toolcontract.ProgramMemory) (*BytePipe, error) {
	if ctx == nil || budget == nil {
		return nil, errors.New("byte pipe requires context and ingress budget")
	}
	stream := newPipeRendezvous(ctx)
	p := &BytePipe{identity: &consumerEndpointIdentity{marker: 1}, stream: stream, budget: budget, done: make(chan struct{}), digest: sha256.New(), completion: StreamCompletion{Termination: StreamOpen}, mapping: pipeMappings{ctx: ctx, memory: memory, settled: make(chan struct{})}}
	go func() {
		select {
		case <-ctx.Done():
			p.finish(StreamCanceled, ctx.Err())
		case <-p.done:
		}
	}()
	return p, nil
}

func (p *BytePipe) Read(b []byte) (int, error) {
	return p.read(p.stream.lifetime, b, nil)
}

func (p *BytePipe) read(ctx context.Context, b []byte, activity *ExecutionWait) (int, error) {
	n, err := p.stream.read(ctx, b, activity)
	return n, p.pipeError(err)
}

func (p *BytePipe) pipeError(err error) error {
	if !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminalError != nil {
		return p.terminalError
	}
	return err
}

// Write charges all attempted ingress before any byte can reach the consumer.
// The caller must screen bytes before this boundary; raw secrets are never
// supplied here for retrospective redaction.
func (p *BytePipe) Write(b []byte) (int, error) {
	n, _, err := p.write(b, nil)
	return n, err
}

// WriteLocated is Write that also reports the stream offset of b.
func (p *BytePipe) WriteLocated(b []byte) (int, int64, error) { return p.write(b, nil) }

func (p *BytePipe) write(b []byte, activity *ExecutionWait) (int, int64, error) {
	if err := p.stream.acquireWriter(activity); err != nil {
		return 0, 0, err
	}
	defer p.stream.releaseWriter()
	p.mu.Lock()
	terminal, terminalErr, start := p.completion.Termination, p.terminalError, p.completion.EmittedBytes
	p.mu.Unlock()
	if terminal != StreamOpen {
		if terminalErr == nil {
			terminalErr = io.ErrClosedPipe
		}
		return 0, start, terminalErr
	}
	if len(b) == 0 {
		return 0, start, nil
	}
	if err := p.budget.Charge(int64(len(b))); err != nil {
		p.finish(StreamBudgetExceeded, err)
		return 0, start, err
	}
	p.mu.Lock()
	p.completion.InputBytes += int64(len(b))
	p.mu.Unlock()
	n, err := p.stream.write(b, activity)
	p.mu.Lock()
	_, _ = p.digest.Write(b[:n])
	p.completion.EmittedBytes += int64(n)
	p.mu.Unlock()
	return n, start, p.pipeError(err)
}

// RecordSourceMapping keeps one mapping of emitted bytes. Mappings are
// provenance only: when memory runs out the pipe drops all of them rather
// than failing the producer.
func (p *BytePipe) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	keep := &p.mapping
	if keep.memory == nil || keep.disabled {
		return nil
	}
	if m.OutputStartByte < 0 || m.OutputEndByte <= m.OutputStartByte || m.OutputEndByte > p.completion.EmittedBytes {
		return nil
	}
	charge := 256 + int64(len(m.Source.SourceID)+len(m.Source.Revision)+len(m.Source.SHA256))
	release, err := keep.memory.ReserveProgramState(keep.ctx, charge)
	if err != nil {
		keep.dropLocked()
		keep.disabled = true
		return nil
	}
	keep.records = append(keep.records, m)
	keep.releases = append(keep.releases, release)
	return nil
}

// MappingAdmission is the memory that nested writers charge their span tables to.
func (p *BytePipe) MappingAdmission() (context.Context, *toolcontract.ProgramMemory) {
	return p.mapping.ctx, p.mapping.memory
}

// ReleaseMappings frees retained mappings after every stage has settled.
func (p *BytePipe) ReleaseMappings() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mapping.dropLocked()
	p.mapping.disabled = true
}

func (m *pipeMappings) dropLocked() {
	for _, release := range m.releases {
		release()
	}
	m.records, m.releases = nil, nil
}

func (p *BytePipe) settledMappings(ctx context.Context) ([]toolcontract.StreamSourceMapping, error) {
	if err := p.CloseConsumer(); err != nil {
		return nil, err
	}
	select {
	case <-p.mapping.settled:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mapping.records[:len(p.mapping.records):len(p.mapping.records)], nil
}

// CloseProducer records EOF only when the producing operation reports a
// complete stream. Utility exit status and publication eligibility remain in
// ProducerResult; neither is inferred from this close.
func (p *BytePipe) CloseProducer(err error) error {
	defer p.mapping.once.Do(func() { close(p.mapping.settled) })
	if waitErr := p.stream.acquireWriter(nil); waitErr != nil {
		return waitErr
	}
	defer p.stream.releaseWriter()
	if err != nil {
		p.finish(StreamIncomplete, err)
	} else {
		p.finish(StreamEOF, nil)
	}
	return nil
}

func (p *BytePipe) CloseConsumer() error {
	p.finish(StreamConsumerClosed, consumerClosedError{endpoint: p.identity})
	return nil
}

// ConsumerClosed reports whether the downstream consumer closed this pipe.
func (p *BytePipe) ConsumerClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completion.Termination == StreamConsumerClosed
}

// ConsumerEnd is the stdin endpoint for Streams.Input; Close is CloseConsumer.
func (p *BytePipe) ConsumerEnd() io.ReadCloser { return pipeConsumer{pipe: p} }

type pipeConsumer struct {
	pipe *BytePipe
	wait *ExecutionWait
}

func (c pipeConsumer) Read(b []byte) (int, error) {
	return c.pipe.read(c.pipe.stream.lifetime, b, c.wait)
}
func (c pipeConsumer) ReadContext(ctx context.Context, b []byte) (int, error) {
	return c.pipe.read(ctx, b, c.wait)
}
func (c pipeConsumer) InputReady() bool {
	p := c.pipe.stream
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.data) > 0 || p.closed
}
func (c pipeConsumer) Close() error { return c.pipe.CloseConsumer() }

// SettledSourceMappings implements MappedInput.
func (c pipeConsumer) SettledSourceMappings(ctx context.Context) ([]toolcontract.StreamSourceMapping, error) {
	return c.pipe.settledMappings(ctx)
}

func (p *BytePipe) finish(termination StreamTermination, err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.completion.Termination = termination
		p.terminalError = err
		p.mu.Unlock()
		// Wake blocked endpoints after recording the terminal stream facts.
		p.stream.close(err)
		close(p.done)
	})
}

// Completion is detached side metadata. It is final after the writer returns
// and the producer has settled, rather than merely after downstream stops.
func (p *BytePipe) Completion() StreamCompletion {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.completion
	out.EmittedSHA256 = hex.EncodeToString(p.digest.Sum(nil))
	return out
}
