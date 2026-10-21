package commandprogram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"sync"
)

// ErrConsumerClosed distinguishes a deliberate downstream stop from producer
// failure. The evaluator still settles the producer independently of this pipe.
var ErrConsumerClosed = errors.New("command stream consumer closed")

// BytePipe carries already-screened bytes without a display projection or an
// intermediate buffer. io.Pipe provides backpressure even for slow consumers.
type BytePipe struct {
	reader        *io.PipeReader
	writer        *io.PipeWriter
	budget        *ByteBudget
	done          chan struct{}
	once          sync.Once
	writeMu       sync.Mutex
	mu            sync.Mutex
	digest        hash.Hash
	completion    StreamCompletion
	terminalError error
}

func NewBytePipe(ctx context.Context, budget *ByteBudget) (*BytePipe, error) {
	if ctx == nil || budget == nil {
		return nil, errors.New("byte pipe requires context and ingress budget")
	}
	r, w := io.Pipe()
	p := &BytePipe{reader: r, writer: w, budget: budget, done: make(chan struct{}), digest: sha256.New(), completion: StreamCompletion{Termination: StreamOpen}}
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
	n, err := p.reader.Read(b)
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
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	terminal, terminalErr := p.completion.Termination, p.terminalError
	p.mu.Unlock()
	if terminal != StreamOpen {
		if terminalErr == nil {
			terminalErr = io.ErrClosedPipe
		}
		return 0, terminalErr
	}
	if err := p.budget.Charge(int64(len(b))); err != nil {
		p.finish(StreamBudgetExceeded, err)
		return 0, err
	}
	p.mu.Lock()
	p.completion.InputBytes += int64(len(b))
	p.mu.Unlock()
	n, err := p.writer.Write(b)
	p.mu.Lock()
	_, _ = p.digest.Write(b[:n])
	p.completion.EmittedBytes += int64(n)
	p.mu.Unlock()
	return n, p.pipeError(err)
}

// CloseProducer records EOF only when the producing operation reports a
// complete stream. Utility exit status and publication eligibility remain in
// ProducerResult; neither is inferred from this close.
func (p *BytePipe) CloseProducer(err error) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err != nil {
		p.finish(StreamIncomplete, err)
	} else {
		p.finish(StreamEOF, nil)
	}
	return nil
}

func (p *BytePipe) CloseConsumer() error {
	p.finish(StreamConsumerClosed, ErrConsumerClosed)
	return nil
}

// ConsumerEnd is the stdin endpoint for Streams.Input; Close is CloseConsumer.
func (p *BytePipe) ConsumerEnd() io.ReadCloser { return pipeConsumer{p} }

type pipeConsumer struct{ pipe *BytePipe }

func (c pipeConsumer) Read(b []byte) (int, error) { return c.pipe.Read(b) }
func (c pipeConsumer) Close() error               { return c.pipe.CloseConsumer() }

func (p *BytePipe) finish(termination StreamTermination, err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.completion.Termination = termination
		p.terminalError = err
		p.mu.Unlock()
		// Close both ends so neither an idle writer nor a blocked reader leaks.
		_ = p.writer.CloseWithError(err)
		if termination != StreamEOF {
			_ = p.reader.CloseWithError(err)
		}
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
