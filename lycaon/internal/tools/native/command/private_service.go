package command

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine/privateguesthost"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// Each live environment reserves up to one GiB.
const maxPrivateServiceSessions = 4

type privateServiceClient interface {
	RunInput(context.Context, []string, []byte) (serviceguest.Result, error)
	Close() error
}

type privateServiceSession struct {
	mu       sync.Mutex
	client   privateServiceClient
	starting chan struct{}
	cancel   context.CancelFunc
	closed   bool
	active   int
	slots    chan struct{}
}

func openPrivateService(ctx context.Context) (privateServiceClient, error) {
	client, err := privateguesthost.OpenPrivateGuest(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (s *privateServiceSession) acquire(ctx context.Context, open func(context.Context) (privateServiceClient, error)) (privateServiceClient, error) {
	for {
		s.mu.Lock()
		switch {
		case s.closed:
			s.mu.Unlock()
			return nil, context.Canceled
		case s.client != nil:
			client := s.client
			s.mu.Unlock()
			return client, nil
		case s.starting != nil:
			starting := s.starting
			s.mu.Unlock()
			select {
			case <-starting:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			if s.slots != nil {
				select {
				case s.slots <- struct{}{}:
				default:
					s.mu.Unlock()
					return nil, toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{"limit": "private_service_sessions", "maximum": cap(s.slots), "observed": cap(s.slots) + 1})
				}
			}
			start, cancel := context.WithTimeout(ctx, time.Minute)
			s.cancel, s.starting = cancel, make(chan struct{})
			s.mu.Unlock()
			client, err := open(start)
			cancel()
			s.mu.Lock()
			if s.closed && client != nil {
				err = errors.Join(context.Canceled, client.Close())
				client = nil
			}
			if client == nil && s.slots != nil {
				<-s.slots
			}
			s.client = client
			close(s.starting)
			s.cancel, s.starting = nil, nil
			s.mu.Unlock()
			return client, err
		}
	}
}

func (s *privateServiceSession) close() error {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	starting, client := s.starting, s.client
	s.mu.Unlock()
	if starting != nil {
		<-starting
	}
	if client != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.client != nil {
			return s.releaseClient()
		}
	}
	return nil
}

// The caller holds mu until the runtime has relinquished its slot.
func (s *privateServiceSession) releaseClient() error {
	err := s.client.Close()
	if err == nil {
		s.client = nil
		if s.slots != nil {
			<-s.slots
		}
	}
	return err
}

func (s *privateServiceSession) releaseIdle() (toolcontract.ProducerResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != 0 {
		err := errors.New("private service jobs are running")
		return readFailure(err), err
	}
	if s.client == nil {
		return readResult(0), nil
	}
	if err := s.releaseClient(); err != nil {
		return ownerFailure(err), err
	}
	result := readResult(0)
	result.EffectCertainty = toolcontract.EffectCompleted
	return result, nil
}

func (s *privateServiceSession) execute(ctx context.Context, req commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	args := req.Argv.Values()
	if len(args) == 2 && args[1] == "release" {
		return s.releaseIdle()
	}
	if len(args) < 3 || args[1] != "--" || args[2] == "" {
		const code = "COMMAND_OPERAND_INVALID"
		return refusedResult(code), toolrejection.RejectInvalidArguments(code, map[string]any{"command": "private-service", "expected": "-- PROGRAM [ARG ...]"})
	}
	s.mu.Lock()
	s.active++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active--; s.mu.Unlock() }()
	input, err := io.ReadAll(io.LimitReader(streams.Reader(0), serviceguest.MaxInput+1))
	if err != nil {
		return readFailure(err), err
	}
	if len(input) > serviceguest.MaxInput {
		const code = "COMMAND_LIMIT_EXCEEDED"
		return refusedResult(code), toolrejection.RejectInvalidArguments(code, map[string]any{"limit": "private_service_input_bytes", "maximum": serviceguest.MaxInput, "observed": len(input)})
	}
	client, err := s.acquire(ctx, openPrivateService)
	if errors.Is(err, serviceguest.ErrUnavailable) {
		const code = "COMMAND_PLATFORM_UNAVAILABLE"
		return refusedResult(code), toolrejection.RejectInvalidArguments(code, map[string]any{"command": "private-service", "reason": err.Error()})
	}
	if err != nil {
		return ownerFailure(err), err
	}
	output, err := client.RunInput(ctx, args[2:], input)
	if err != nil {
		return ownerFailure(err), err
	}
	if _, err := nonNilWriter(streams.Output).Write(output.Stdout); err != nil {
		return ownerFailure(err), err
	}
	if _, err := nonNilWriter(streams.Error).Write(output.Stderr); err != nil {
		return ownerFailure(err), err
	}
	complete := !output.StdoutTruncated && !output.StderrTruncated
	return toolcontract.ProducerResult{Status: output.ExitCode, Terminal: toolcontract.ProducerCompleted, Complete: complete, PublicationEligible: complete && output.ExitCode == 0, EffectCertainty: toolcontract.EffectCompleted}, nil
}
