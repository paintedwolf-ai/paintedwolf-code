package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"golang.org/x/sys/unix"
)

type jobs struct {
	config  configuration
	mu      sync.Mutex
	records map[string]*job
	closed  bool
	order   []string
}
type job struct {
	id             string
	cmd            *exec.Cmd
	group          *os.File
	done           chan struct{}
	mu             sync.Mutex
	result         serviceguest.Result
	stdout, stderr output
}
type output struct {
	mu        sync.Mutex
	bytes     []byte
	truncated bool
}

func (o *output) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := min(len(b), serviceguest.MaxOutput-len(o.bytes))
	o.bytes = append(o.bytes, b[:n]...)
	o.truncated = o.truncated || n < len(b)
	return len(b), nil
}
func (o *output) snapshot() ([]byte, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.bytes...), o.truncated
}

func newJobs(c configuration) *jobs { return &jobs{config: c, records: make(map[string]*job)} }

func (s *jobs) Start(ctx context.Context, id string, argv []string, input []byte) (serviceguest.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.records[id] != nil {
		return serviceguest.Job{}, errors.New("private service launch unavailable")
	}
	active := 0
	for _, j := range s.records {
		select {
		case <-j.done:
		default:
			active++
		}
	}
	if active >= 8 {
		return serviceguest.Job{}, errors.New("private service session has eight active jobs")
	}
	if len(s.order) >= 32 {
		for i, old := range s.order {
			select {
			case <-s.records[old].done:
				delete(s.records, old)
				s.order = append(s.order[:i], s.order[i+1:]...)
			default:
				continue
			}
			break
		}
	}
	path := filepath.Join(s.config.groups, id)
	if err := os.Mkdir(path, 0700); err != nil {
		return serviceguest.Job{}, err
	}
	group, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		return serviceguest.Job{}, err
	}
	j := &job{id: id, group: group, done: make(chan struct{})}
	if err := s.launch(ctx, j, argv, input); err != nil {
		_ = group.Close()
		_ = os.Remove(path)
		return serviceguest.Job{}, err
	}
	s.records[id] = j
	s.order = append(s.order, id)
	go func() {
		_ = j.cmd.Wait()
		code := j.cmd.ProcessState.ExitCode()
		if status, ok := j.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
		j.mu.Lock()
		_ = j.killGroup()
		_ = j.group.Close()
		j.group = nil
		j.result.ExitCode = code
		close(j.done)
		j.mu.Unlock()
		_ = os.Remove(path)
	}()
	return j.snapshot(), nil
}

func (s *jobs) launch(ctx context.Context, j *job, argv []string, input []byte) error {
	box := confine.Confinement{SessionScratchRoot: s.config.scratch, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
		Process: confine.ProcessAttributes{Limits: []confine.ResourceLimit{{Resource: "core", Soft: 0, Hard: 0}, {Resource: "nofile", Soft: 256, Hard: 256}, {Resource: "fsize", Soft: 128 << 20, Hard: 128 << 20}}}}
	cmd, release, err := confine.Command(ctx, s.config.helper, argv[0], argv[1:], box)
	if err != nil {
		return err
	}
	defer release()
	cmd.Stdin = bytes.NewReader(input)
	cmd.Dir = s.config.scratch
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/work/home", "TMPDIR=/work/tmp", "TEMP=/work/tmp", "TMP=/work/tmp", "XDG_CACHE_HOME=/work/cache", "XDG_DATA_HOME=/work/data", "LANG=C", "LC_ALL=C"}
	cmd.Stdout, cmd.Stderr = &j.stdout, &j.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: workUID, Gid: workGID}, Setpgid: true, UseCgroupFD: true, CgroupFD: int(j.group.Fd())}
	if err := cmd.Start(); err != nil {
		return err
	}
	j.cmd = cmd
	return nil
}

func (j *job) killGroup() error {
	if j.group == nil {
		return nil
	}
	fd, err := unix.Openat(int(j.group.Fd()), "cgroup.kill", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	_, err = unix.Write(fd, []byte("1"))
	return err
}

func (j *job) snapshot() serviceguest.Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := j.result
	result.Stdout, result.StdoutTruncated = j.stdout.snapshot()
	result.Stderr, result.StderrTruncated = j.stderr.snapshot()
	state := serviceguest.Running
	select {
	case <-j.done:
		state = serviceguest.Exited
	default:
	}
	return serviceguest.Job{ID: j.id, State: state, Result: result}
}

func (s *jobs) find(id string) (*job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.records[id]; j != nil {
		return j, nil
	}
	return nil, errors.New("private service job unavailable")
}
func (s *jobs) Status(_ context.Context, id string) (serviceguest.Job, error) {
	j, err := s.find(id)
	if err != nil {
		return serviceguest.Job{}, err
	}
	return j.snapshot(), nil
}
func (s *jobs) Wait(ctx context.Context, id string) (serviceguest.Job, error) {
	j, err := s.find(id)
	if err != nil {
		return serviceguest.Job{}, err
	}
	select {
	case <-ctx.Done():
		return serviceguest.Job{}, ctx.Err()
	case <-j.done:
	case <-time.After(time.Second):
	}
	return j.snapshot(), nil
}
func (s *jobs) Stop(ctx context.Context, id string) (serviceguest.Job, error) {
	j, err := s.find(id)
	if err != nil {
		return serviceguest.Job{}, err
	}
	_ = j.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-j.done:
		return j.snapshot(), nil
	case <-ctx.Done():
		return serviceguest.Job{}, ctx.Err()
	case <-time.After(2 * time.Second):
	}
	j.mu.Lock()
	err = j.killGroup()
	j.mu.Unlock()
	if err != nil {
		return serviceguest.Job{}, fmt.Errorf("stop private service: %w", err)
	}
	select {
	case <-j.done:
		return j.snapshot(), nil
	case <-ctx.Done():
		return serviceguest.Job{}, ctx.Err()
	}
}

func (s *jobs) Close() error {
	s.mu.Lock()
	s.closed = true
	all := make([]*job, 0, len(s.records))
	for _, j := range s.records {
		all = append(all, j)
	}
	s.mu.Unlock()
	var result error
	for _, j := range all {
		j.mu.Lock()
		result = errors.Join(result, j.killGroup())
		j.mu.Unlock()
	}
	for _, j := range all {
		<-j.done
	}
	return result
}
