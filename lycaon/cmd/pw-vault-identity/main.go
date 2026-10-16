// pw-vault-identity holds each user's credential vault identity for the
// installed Painted Wolf Code engine. systemd starts it on demand from its
// socket unit as a dynamic user, so the identities at rest belong to no
// login user, and it releases one only to a verified engine whose parent is
// the installed desktop app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
)

const (
	// systemd passes activated sockets from this descriptor on.
	listenFDsStart = 3
	idleExit       = 2 * time.Minute
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pw-vault-identity: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pw-vault-identity", flag.ContinueOnError)
	socket := fs.String("socket", "", "listen on this socket path instead of the systemd-activated descriptor")
	state := fs.String("state", os.Getenv("STATE_DIRECTORY"), "directory holding per-user identities")
	manifest := fs.String("manifest", pkgmanifest.DefaultPath, "installed executable manifest")
	procRoot := fs.String("proc", "/proc", "process filesystem root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *state == "" {
		return errors.New("a state directory is required (STATE_DIRECTORY or --state)")
	}
	if err := osprocess.DisableDumps(); err != nil {
		return fmt.Errorf("disable dumps: %w", err)
	}
	listener, err := listen(context.Background(), *socket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	server := &Server{
		Store:    &Store{Root: *state},
		Verifier: launchverify.Verifier{ProcRoot: *procRoot},
		Manifest: *manifest,
		PeerOf:   peerCredentials,
		IdleExit: idleExit,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	return server.Serve(ctx, listener)
}

// listen takes the socket systemd activated, or binds path for a manual run.
func listen(ctx context.Context, path string) (net.Listener, error) {
	if path != "" {
		_ = os.Remove(path)
		var config net.ListenConfig
		listener, err := config.Listen(ctx, "unix", path)
		if err != nil {
			return nil, err
		}
		// Any user may connect; the service decides from peer credentials.
		if err := os.Chmod(path, 0o666); err != nil { // #nosec G302 -- the socket is the public door by design.
			_ = listener.Close()
			return nil, err
		}
		return listener, nil
	}
	if pid, err := strconv.Atoi(os.Getenv("LISTEN_PID")); err != nil || pid != os.Getpid() {
		return nil, errors.New("no activated socket: LISTEN_PID does not name this process")
	}
	if count, err := strconv.Atoi(os.Getenv("LISTEN_FDS")); err != nil || count != 1 {
		return nil, errors.New("exactly one activated socket is expected")
	}
	file := os.NewFile(uintptr(listenFDsStart), "vault-identity.sock")
	if file == nil {
		return nil, errors.New("the activated socket descriptor is not open")
	}
	defer func() { _ = file.Close() }()
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("adopt activated socket: %w", err)
	}
	return listener, nil
}
