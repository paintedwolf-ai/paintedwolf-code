package serviceguest

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"sync"
)

type Executor interface {
	Start(context.Context, string, []string, []byte) (Job, error)
	Status(context.Context, string) (Job, error)
	Wait(context.Context, string) (Job, error)
	Stop(context.Context, string) (Job, error)
	Close() error
}

// Serve ties the workload lifetime to the host's retained channel.
func Serve(ctx context.Context, channel io.ReadWriteCloser, executor Executor) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = channel.Close() })
	defer stop()
	defer func() { _ = channel.Close() }()
	var first request
	if err := readFrame(channel, &first); err != nil {
		return err
	}
	token, err := hex.DecodeString(first.Challenge)
	if err != nil || len(token) != 32 || first.Version != Version || first.Op != "hello" || first.Sequence != 1 || first.ID != "" || len(first.Argv) != 0 || len(first.Input) != 0 {
		return errors.New("invalid private service handshake")
	}
	if err := writeFrame(channel, response{Version: Version, Sequence: first.Sequence, Challenge: first.Challenge}); err != nil {
		return err
	}
	var wg sync.WaitGroup
	var writer sync.Mutex
	limit := make(chan struct{}, 32)
	defer func() { cancel(); _ = channel.Close(); _ = executor.Close(); wg.Wait() }()
	previous := first.Sequence
	for {
		var req request
		if err := readFrame(channel, &req); err != nil {
			return err
		}
		if req.Version != Version || req.Sequence <= previous || req.Challenge != "" {
			return errors.New("invalid private service sequence")
		}
		previous = req.Sequence
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		respond := func(job Job, err error) {
			result := response{Version: Version, Sequence: req.Sequence, Job: &job}
			if err != nil {
				result.Job = nil
				result.Error = err.Error()
			}
			writer.Lock()
			err = writeFrame(channel, result)
			writer.Unlock()
			if err != nil {
				cancel()
			}
			<-limit
		}
		id, err := hex.DecodeString(req.ID)
		if err != nil || len(id) != 16 || req.ID != hex.EncodeToString(id) || (req.Op == "start") != ValidArgv(req.Argv) || len(req.Input) > MaxInput || (req.Op != "start" && (len(req.Argv) > 0 || len(req.Input) > 0)) {
			respond(Job{}, errors.New("invalid private service request"))
			continue
		}
		switch req.Op {
		case "start":
			respond(executor.Start(ctx, req.ID, req.Argv, req.Input))
		case "status":
			respond(executor.Status(ctx, req.ID))
		case "stop":
			wg.Add(1)
			go func() { defer wg.Done(); respond(executor.Stop(ctx, req.ID)) }()
		case "wait":
			wg.Add(1)
			go func() { defer wg.Done(); respond(executor.Wait(ctx, req.ID)) }()
		default:
			respond(Job{}, errors.New("unknown private service operation"))
		}
	}
}
