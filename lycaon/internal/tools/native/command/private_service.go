package command

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

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
}

func openPrivateService(ctx context.Context) (privateServiceClient, error) {
	client, err := confine.OpenPrivateGuest(ctx)
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
			start, cancel := context.WithCancel(ctx)
			s.cancel, s.starting = cancel, make(chan struct{})
			s.mu.Unlock()
			client, err := open(start)
			cancel()
			s.mu.Lock()
			if s.closed && client != nil {
				err = errors.Join(context.Canceled, client.Close())
				client = nil
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
		return client.Close()
	}
	return nil
}

func (s *privateServiceSession) execute(ctx context.Context, req commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	args := req.Argv.Values()
	if len(args) < 3 || args[1] != "--" || args[2] == "" {
		const code = "COMMAND_OPERAND_INVALID"
		return refusedResult(code), toolrejection.RejectInvalidArguments(code, map[string]any{"command": "private-service", "expected": "-- PROGRAM [ARG ...]"})
	}
	input, err := io.ReadAll(io.LimitReader(streams.Reader(0), serviceguest.MaxInput+1))
	if err != nil {
		return readFailure(err), err
	}
	if len(input) > serviceguest.MaxInput {
		const code = "COMMAND_LIMIT_EXCEEDED"
		return refusedResult(code), toolrejection.RejectInvalidArguments(code, map[string]any{"limit": "private_service_input_bytes", "maximum": serviceguest.MaxInput, "observed": len(input)})
	}
	client, err := s.acquire(ctx, openPrivateService)
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
