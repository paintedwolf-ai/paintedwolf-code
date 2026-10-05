package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/hostlock"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/scan/scanworker"
	"github.com/lycaon/lycaon/internal/startupprotocol"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ssh-proxy-command" {
		os.Exit(runSSHProxyCommand(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == execpkg.ReaperCommand {
		os.Exit(execpkg.RunReaper(os.Stdin))
	}
	confine.EnableAutoConfine()

	os.Exit(runCLI(os.Args[1:]))
}

// runCLI manages the signal-aware process context.
func runCLI(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, args); err != nil {
		fmt.Fprintf(os.Stderr, "pw: %v\n", err)
		// Store lock failures use a stable process status.
		if errors.Is(err, hostlock.ErrStoreInstanceLocked) {
			return hostlock.ExitCodeStoreInstanceLocked
		}
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string) error {
	kind, verb, rest := classifyArgs(args)
	if kind == dispatchOpen {
		return runOpen(ctx, rest)
	}
	switch verb {
	case "serve":
		dbPath, err := parseServeDBFlag(rest)
		if err != nil {
			return err
		}
		return runServe(ctx, dbPath)
	case "open":
		return runOpen(ctx, rest)
	case "ls":
		return runLs(ctx, rest)
	case "logs":
		return runLogs(rest)
	case "completion":
		return runCompletion(rest)
	case "diagnostics":
		return runDiagnostics(ctx, rest)
	case "credentials":
		return runCredentials(rest)
	case "git-credential-osxkeychain":
		if len(rest) != 1 {
			return errors.New("usage: pw git-credential-osxkeychain {get|store|erase}")
		}
		return gitcredential.Run(rest[0], os.Stdin, os.Stdout)
	case "scan":
		if len(rest) < 1 || rest[0] != "engines" {
			return fmt.Errorf("usage: pw scan engines {list|check|validate} [--project DIR]")
		}
		return runScanEngines(rest[1:])
	case "internal-scan-worker":
		return scanworker.Serve(ctx, os.Stdin, os.Stdout)
	case "workflow":
		installAuthorCatalog(ctx)
		return runWorkflow(ctx, rest)
	case "extensions":
		return runExtensions(ctx, rest)
	case "rules":
		installAuthorCatalog(ctx)
		return runRules(rest)
	case "prompts":
		installAuthorCatalog(ctx)
		return runPrompts(ctx, rest)
	case "browser":
		return runBrowser(ctx, rest)
	case "decide":
		return runDecide(ctx, rest)
	case "pack":
		installAuthorCatalog(ctx)
		return runPack(ctx, rest)
	default:
		return fmt.Errorf("unknown command %q", verb)
	}
}

func parseServeDBFlag(args []string) (string, error) {
	dbPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--db":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--db requires a path argument")
			}
			dbPath = args[i+1]
			i++
		default:
			return "", fmt.Errorf("unknown argument %q", args[i])
		}
	}
	return dbPath, nil
}

// runServe stops on a signal from any phase: during the build it cancels
// in-flight children and stops between steps; once serving, Run drains.
func runServe(signals context.Context, dbPath string) error {
	defer observability.FlushConsole() //nolint:contextcheck // flushing must outlive the serve context
	ctx, cancel := context.WithCancel(signals)
	defer cancel()
	startReaper() //nolint:contextcheck // the reaper outlives the serve context
	cfg := app.DefaultConfig()
	startup, err := startupprotocol.FromEnvironment(os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	if startup != nil {
		defer startup.Close()
		cfg.Startup = startup
	}
	cfg.DBPath = dbPath
	if strings.TrimSpace(os.Getenv(credentialstore.UnlockStdinEnv)) != "" {
		if err := credentialstore.ReadUnlockFrame(os.Stdin); err != nil {
			return err
		}
	}
	if strings.TrimSpace(os.Getenv(startupprotocol.ControlStdinEnv)) != "" {
		go func() {
			_ = startupprotocol.ReadControlShutdown(os.Stdin)
			cancel()
		}()
	}
	if root := strings.TrimSpace(os.Getenv("LYCAON_CONFIG_ROOT")); root != "" {
		cfg.ConfigRoot = root
	}
	serveApp, err := app.Build(ctx, cfg)
	credentialstore.ClearUnlockSecret()
	if ctx.Err() != nil {
		// Shutdown was requested during startup, which is not a startup failure.
		if serveApp != nil {
			_ = serveApp.Close() //nolint:contextcheck // shutdown runs after the serve context ends
		}
		return nil
	}
	if err != nil {
		if startup != nil {
			code := "build_failed"
			switch {
			case errors.Is(err, hostlock.ErrStoreInstanceLocked):
				code = "store_locked"
			case errors.Is(err, credentialstore.ErrVaultLocked):
				code = "credential_vault_locked"
			case errors.Is(err, credentialstore.ErrVaultUninitialized):
				code = "credential_vault_uninitialized"
			case errors.Is(err, credentialstore.ErrVaultUnlockFailed):
				code = "credential_vault_unlock_failed"
			case errors.Is(err, credentialstore.ErrVaultCorrupt):
				code = "credential_vault_corrupt"
			}
			_ = startup.Failed(code)
		}
		return err
	}
	defer func() { _ = serveApp.Close() }() //nolint:contextcheck // shutdown runs after the serve context ends
	return serveApp.Run(ctx)
}

// startReaper keeps the engine's children from outliving it; without the
// companion they still end with orderly shutdown, so failure only warns.
func startReaper() {
	self, err := os.Executable()
	if err == nil {
		err = execpkg.StartReaper(self, execpkg.ReaperCommand)
	}
	if err != nil {
		slog.Warn("process reaper unavailable; engine children can outlive an abrupt exit", "err", err)
	}
}
