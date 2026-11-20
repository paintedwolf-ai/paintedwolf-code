package serviceguest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

type pending struct{ reply chan response }

type Client struct {
	transport io.ReadWriteCloser
	mu        sync.Mutex
	next      uint64
	active    map[uint64]pending
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	send      sync.Mutex
}

// Open checks protocol readiness on the adapter's retained channel.
func Open(ctx context.Context, transport io.ReadWriteCloser) (*Client, error) {
	c := &Client{transport: transport, active: make(map[uint64]pending), done: make(chan struct{})}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.Join(err, transport.Close())
	}
	go c.receive()
	token := hex.EncodeToString(nonce[:])
	reply, err := c.call(ctx, request{Op: "hello", Challenge: token})
	if err == nil && reply.Challenge != token {
		err = errors.New("private service handshake failed")
	}
	if err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.done); c.closeErr = c.transport.Close() })
	return c.closeErr
}

func (c *Client) receive() {
	defer func() { _ = c.Close() }()
	for {
		var r response
		if readFrame(c.transport, &r) != nil || r.Version != Version || r.Sequence == 0 {
			return
		}
		c.mu.Lock()
		waiter, ok := c.active[r.Sequence]
		if ok {
			delete(c.active, r.Sequence)
		}
		c.mu.Unlock()
		if ok {
			waiter.reply <- r
		}
	}
}

func (c *Client) call(ctx context.Context, req request) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, err
	}
	c.send.Lock()
	c.mu.Lock()
	c.next++
	req.Version, req.Sequence = Version, c.next
	waiter := pending{reply: make(chan response, 1)}
	c.active[req.Sequence] = waiter
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.active, req.Sequence); c.mu.Unlock() }()
	if conn, ok := c.transport.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	}
	err := writeFrame(c.transport, req)
	c.send.Unlock()
	if err != nil {
		_ = c.Close()
		return response{}, err
	}
	select {
	case reply := <-waiter.reply:
		if reply.Error != "" {
			return response{}, errors.New(reply.Error)
		}
		return reply, nil
	case <-c.done:
		return response{}, io.ErrClosedPipe
	case <-ctx.Done():
		return response{}, ctx.Err()
	}
}

func (c *Client) action(ctx context.Context, req request) (Job, error) {
	r, err := c.call(ctx, req)
	if err != nil {
		return Job{}, err
	}
	if r.Job == nil || len(r.Job.Stdout) > MaxOutput || len(r.Job.Stderr) > MaxOutput || r.Job.ID != req.ID || (r.Job.State != Running && r.Job.State != Exited) {
		return Job{}, errors.New("invalid private service job response")
	}
	return *r.Job, nil
}

// Start gives a service an unpredictable handle before its launch is submitted.
func (c *Client) Start(ctx context.Context, argv []string) (Job, error) {
	return c.start(ctx, argv, nil)
}

func (c *Client) start(ctx context.Context, argv []string, input []byte) (Job, error) {
	if !ValidArgv(argv) || len(input) > MaxInput {
		return Job{}, errors.New("invalid private service arguments")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Job{}, err
	}
	jobID := hex.EncodeToString(id[:])
	job, err := c.action(ctx, request{Op: "start", Argv: argv, Input: input, ID: jobID})
	if err != nil && ctx.Err() != nil {
		c.stopCanceled(ctx, jobID)
	}
	return job, err
}

func (c *Client) Status(ctx context.Context, id string) (Job, error) {
	return c.action(ctx, request{Op: "status", ID: id})
}
func (c *Client) Stop(ctx context.Context, id string) (Job, error) {
	return c.action(ctx, request{Op: "stop", ID: id})
}
func (c *Client) stopCanceled(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = c.Stop(ctx, id)
}

// Run waits for one foreground job and cancels only that job.
func (c *Client) Run(ctx context.Context, argv []string) (Result, error) {
	return c.RunInput(ctx, argv, nil)
}

func (c *Client) RunInput(ctx context.Context, argv []string, input []byte) (Result, error) {
	job, err := c.start(ctx, argv, input)
	if err != nil {
		return Result{}, err
	}
	id := job.ID
	for job.State == Running {
		job, err = c.action(ctx, request{Op: "wait", ID: job.ID})
		if err != nil {
			if ctx.Err() != nil {
				c.stopCanceled(ctx, id)
			}
			return Result{}, err
		}
	}
	return job.Result, nil
}
