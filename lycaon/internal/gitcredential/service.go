package gitcredential

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/githelper"
)

// SocketEnv carries the per-invocation service socket to the helper Git runs.
const SocketEnv = "LYCAON_GIT_CREDENTIAL_SOCKET"

const maxOperation = 16

// ServeOptions bind one service to the Git invocation it serves.
type ServeOptions struct {
	// GitBinary is the bundled git the helper must descend from.
	GitBinary string
	// Store answers helper operations; the vault when nil.
	Store Backend
	// Scope, when set, limits answers to the invocation's admitted recipients.
	Scope *Scope
}

// Service answers the credential-helper protocol for one Git invocation over
// a private socket.
type Service struct {
	ctx       context.Context
	cancel    context.CancelFunc
	listener  *net.UnixListener
	directory string
	store     Backend
	binding   *githelper.Binding
	deadline  time.Duration
	accepted  func()
	wg        sync.WaitGroup
	once      sync.Once
}

// Serve starts a service bound to ctx; Close releases its socket.
func Serve(ctx context.Context, options ServeOptions) (*Service, error) {
	return serve(ctx, options, nil)
}

func serve(ctx context.Context, options ServeOptions, configure func(*Service)) (*Service, error) {
	binding, err := githelper.NewBinding(options.GitBinary)
	if err != nil {
		return nil, fmt.Errorf("git credential service: %w", err)
	}
	listener, directory, err := githelper.Listen(ctx, "credential")
	if err != nil {
		return nil, fmt.Errorf("git credential service: %w", err)
	}
	store := options.Store
	if store == nil {
		store = VaultBackend()
	}
	if options.Scope != nil {
		store = scopedBackend{inner: store, scope: options.Scope}
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	service := &Service{
		ctx:       serviceCtx,
		cancel:    cancel,
		listener:  listener,
		directory: directory,
		store:     store,
		binding:   binding,
		deadline:  githelper.SetupDeadline,
	}
	if configure != nil {
		configure(service)
	}
	service.wg.Add(1)
	go service.accept()
	context.AfterFunc(ctx, func() { _ = service.Close() })
	return service, nil
}

// Bind names the started process of the served invocation.
func (s *Service) Bind(pid int) error {
	return s.binding.Bind(pid)
}

// Path is the socket the helper connects to.
func (s *Service) Path() string { return s.listener.Addr().String() }

// Close stops accepting, waits for in-flight requests and removes the socket.
func (s *Service) Close() error {
	var err error
	s.once.Do(func() {
		s.cancel()
		s.binding.Close()
		err = s.listener.Close()
		s.wg.Wait()
		_ = os.RemoveAll(s.directory)
	})
	return err
}

func (s *Service) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		if s.accepted != nil {
			s.accepted()
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = conn.Close() }()
			_ = s.handle(conn)
		}()
	}
}

func (s *Service) handle(conn *net.UnixConn) error {
	first, err := s.binding.Verify(s.ctx, conn, time.Now().Add(s.deadline))
	if err != nil {
		return err
	}
	reader := bufio.NewReader(io.LimitReader(io.MultiReader(bytes.NewReader(first), conn), maxCredentialInputBytes+maxOperation+2))
	operation, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	operation = strings.TrimSuffix(operation, "\n")
	if len(operation) > maxOperation {
		return errors.New("credential operation exceeds its limit")
	}
	return runWithBackend(operation, reader, conn, s.store)
}

// Forward runs one helper operation against the service Git's environment names.
func Forward(operation, socketPath string, in io.Reader, out io.Writer) error {
	switch operation {
	case "get", "store", "erase":
	default:
		return fmt.Errorf("unsupported credential operation %q", operation)
	}
	conn, err := githelper.Dial(socketPath)
	if err != nil {
		return fmt.Errorf("connect to git credential service: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(githelper.SetupDeadline)); err != nil {
		return err
	}
	request, err := io.ReadAll(io.LimitReader(in, maxCredentialInputBytes+1))
	if err != nil {
		return fmt.Errorf("read credential request: %w", err)
	}
	if len(request) > maxCredentialInputBytes {
		return errors.New("credential request exceeds its limit")
	}
	if err := githelper.WriteIdentified(conn, []byte(operation+"\n")); err != nil {
		return err
	}
	if _, err := conn.Write(request); err != nil {
		return err
	}
	if !strings.HasSuffix(string(request), "\n\n") {
		if _, err := io.WriteString(conn, "\n\n"); err != nil {
			return err
		}
	}
	if err := conn.CloseWrite(); err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(conn, maxCredentialInputBytes+1))
	return err
}
