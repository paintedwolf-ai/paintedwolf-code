package nativegen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/inference/driverenv"
	"github.com/lycaon/lycaon/internal/modelstore"
)

type processSpec struct{ binary, directory, artifact, metallib, pci string }

type rpcProcess struct {
	cmd          *exec.Cmd
	cleanup      func()
	input        io.WriteCloser
	output       io.ReadCloser
	done         chan struct{}
	device       *inference.Device
	owner        string
	generation   uint64
	sequence     uint64
	callMu       sync.Mutex
	mu           sync.Mutex
	exited       bool
	lease        *modelstore.Lease
	reservations []*inference.Reservation
}

func startProcess(ctx context.Context, spec processSpec, lease *modelstore.Lease, device *inference.Device, owner string, generation uint64) (*rpcProcess, error) {
	args := []string{"--model", spec.directory, "--artifact-id", spec.artifact}
	if spec.metallib != "" {
		args = append(args, "--metallib", spec.metallib)
	}
	if spec.pci != "" {
		args = append(args, "--device-pci", spec.pci)
	}
	environment, driverCleanup, err := driverenv.Prepare(configlayout.EngineRoot())
	if err != nil {
		lease.Release()
		return nil, err
	}
	cmd, commandCleanup, err := execpkg.PrepareCommand(context.WithoutCancel(ctx), spec.binary, args, execpkg.ExecOpts{Launch: execpkg.HostLaunch("generation_engine"), NoTimeout: true, AppendEnv: environment})
	if err != nil {
		driverCleanup()
		lease.Release()
		return nil, err
	}
	cleanup := func() { commandCleanup(); driverCleanup() }
	cmd.WaitDelay = 2 * time.Second
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		lease.Release()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		input.Close()
		lease.Release()
		return nil, err
	}
	if err = execpkg.StartPrepared(ctx, cmd); err != nil {
		cleanup()
		input.Close()
		output.Close()
		lease.Release()
		return nil, err
	}
	child := &rpcProcess{cleanup: cleanup, cmd: cmd, input: input, output: output, done: make(chan struct{}), device: device, owner: owner, generation: generation, lease: lease}
	go child.reap()
	return child, nil
}

func (p *rpcProcess) reap() {
	_ = p.cmd.Wait()
	p.cleanup()
	_ = p.input.Close()
	_ = p.output.Close()
	p.mu.Lock()
	p.exited = true
	for _, reservation := range p.reservations {
		reservation.Release()
	}
	p.reservations = nil
	p.lease.Release()
	close(p.done)
	p.mu.Unlock()
}

func (p *rpcProcess) retain(reservations ...*inference.Reservation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		for _, reservation := range reservations {
			reservation.Release()
		}
		return errors.New("native generation: process exited before reservation")
	}
	p.reservations = append(p.reservations, reservations...)
	return nil
}

func (p *rpcProcess) kill()  { _ = p.cmd.Process.Kill() }
func (p *rpcProcess) close() { p.kill(); <-p.done }

// call settles device work before returning a permit to the shared queue.
func (p *rpcProcess) call(ctx context.Context, request command, kind string, priority inference.Priority, compute bool, target any) error {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-p.done:
		return errors.New("native generation: process is closed")
	default:
	}
	if compute {
		if p.device == nil {
			return errors.New("native generation: device admission is unavailable")
		}
		permit, err := p.device.Acquire(ctx, inference.Work{Owner: p.owner, Generation: p.generation, Priority: priority})
		if err != nil {
			return err
		}
		defer permit.Complete()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	p.sequence++
	payload, err := json.Marshal(struct {
		Version uint32  `json:"version"`
		ID      uint64  `json:"id"`
		Command command `json:"command"`
	}{1, p.sequence, request})
	if err != nil {
		return err
	}
	step := request.Method == "advance" || request.Method == "advance_preparation" || request.Method == "prepare" || request.Method == "measure"
	stop := p.watchCancellation(ctx, step)
	defer stop()
	if err = writeProtocolFrame(p.input, payload); err != nil {
		p.close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	reply, err := readProtocolFrame(p.output)
	if err != nil {
		p.close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if stop() {
		p.close()
		return ctx.Err()
	}
	result, err := decodeResponse(reply, p.sequence, kind)
	if err == nil {
		if target == nil {
			target = &struct {
				Kind string `json:"kind"`
			}{}
		}
		err = decodeStrict(result, target)
		if checked, ok := target.(interface{ validate() error }); err == nil && ok {
			err = checked.validate()
		}
	}
	if err != nil {
		var service *ServiceError
		if !errors.As(err, &service) || service.Code == "device" || service.Code == "artifact" {
			p.close()
		}
	}
	if ctx.Err() != nil {
		if !step {
			p.close()
		}
		return ctx.Err()
	}
	return err
}
