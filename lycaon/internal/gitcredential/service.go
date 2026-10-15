package gitcredential

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SocketEnv carries the per-invocation service socket to the helper Git runs.
const SocketEnv = "LYCAON_GIT_CREDENTIAL_SOCKET"

const (
	requestDeadline = 10 * time.Second
	maxOperation    = 16
)

// ServeOptions bind one service to the Git invocation it serves.
type ServeOptions struct {
	// GitBinary is the bundled git the helper must descend from.
	GitBinary string
	// Store answers helper operations; the vault when nil.
	Store backend
}

// Service answers the credential-helper protocol for one Git invocation over
// a private socket. Identity is the calling process: the peer must run this
// executable, share this uid and descend from the served git process.
type Service struct {
	listener  *net.UnixListener
	directory string
	store     backend
	gitBinary string
	helper    string
	wg        sync.WaitGroup
	once      sync.Once
}

// Serve starts a service bound to ctx; Close releases its socket.
func Serve(ctx context.Context, options ServeOptions) (*Service, error) {
	if strings.TrimSpace(options.GitBinary) == "" {
		return nil, errors.New("git credential service requires the served git binary")
	}
	helper, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("git credential service: %w", err)
	}
	gitBinary, err := filepath.EvalSymlinks(options.GitBinary)
	if err != nil {
		return nil, fmt.Errorf("git credential service: %w", err)
	}
	directory, err := os.MkdirTemp(socketRoot(), "pw-git-")
	if err != nil {
		return nil, fmt.Errorf("git credential service: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(directory, "credential"), Net: "unix"})
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("git credential service: %w", err)
	}
	store := options.Store
	if store == nil {
		store = VaultBackend()
	}
	service := &Service{listener: listener, directory: directory, store: store, gitBinary: gitBinary, helper: helper}
	service.wg.Add(1)
	go service.accept()
	context.AfterFunc(ctx, func() { _ = service.Close() })
	return service, nil
}

// socketRoot prefers the per-user runtime directory, whose lifetime is the session.
func socketRoot() string {
	if runtime := filepath.Clean(os.Getenv("XDG_RUNTIME_DIR")); filepath.IsAbs(runtime) {
		if info, err := os.Stat(runtime); err == nil && info.IsDir() {
			return runtime
		}
	}
	return os.TempDir()
}

// Path is the socket the helper connects to.
func (s *Service) Path() string { return s.listener.Addr().String() }

// Close stops accepting, waits for in-flight requests and removes the socket.
func (s *Service) Close() error {
	var err error
	s.once.Do(func() {
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
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = conn.Close() }()
			_ = s.handle(conn)
		}()
	}
}

func (s *Service) handle(conn *net.UnixConn) error {
	if err := conn.SetDeadline(time.Now().Add(requestDeadline)); err != nil {
		return err
	}
	if err := verifyPeer(conn, s.helper, s.gitBinary); err != nil {
		return err
	}
	reader := bufio.NewReader(io.LimitReader(conn, maxCredentialInputBytes+maxOperation+2))
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
	if socketPath == "" {
		return errors.New("git credential service socket is not set")
	}
	switch operation {
	case "get", "store", "erase":
	default:
		return fmt.Errorf("unsupported credential operation %q", operation)
	}
	if !filepath.IsAbs(socketPath) {
		return fmt.Errorf("git credential service socket %q is not an absolute path", socketPath)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Clean(socketPath), Net: "unix"})
	if err != nil {
		return fmt.Errorf("connect to git credential service: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(requestDeadline)); err != nil {
		return err
	}
	request, err := io.ReadAll(io.LimitReader(in, maxCredentialInputBytes+1))
	if err != nil {
		return fmt.Errorf("read credential request: %w", err)
	}
	if len(request) > maxCredentialInputBytes {
		return errors.New("credential request exceeds its limit")
	}
	if _, err := io.WriteString(conn, operation+"\n"); err != nil {
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
