package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people/personactions"
)

const authzUsage = `usage: lycaon-debug authz {verify|events|export|person-actions} --db <store.db> [flags]

Read-only authorization evidence tooling:
  verify          walk every authorization hash chain
  events          print newest authorization events for one session
  export          write JSONL contexts, events, and chain heads for one session
  person-actions  print newest recorded API operations people invoked

events flags:
  --session ID    required session identifier
  --limit N       maximum events (default 500)

export flags:
  --session ID    required session identifier

person-actions flags:
  --limit N       maximum actions (default 500)

Exit: 0 clean, 1 verification finding, 2 usage or I/O error.`

func runAuthz(args []string) error {
	if len(args) == 0 {
		return exitCodeError{code: 2, err: fmt.Errorf("%s", authzUsage)}
	}
	switch args[0] {
	case "verify":
		return runAuthzVerify(args[1:])
	case "events":
		return runAuthzEvents(args[1:])
	case "export":
		return runAuthzExport(args[1:])
	case "person-actions":
		return runAuthzPersonActions(args[1:])
	default:
		return exitCodeError{code: 2, err: fmt.Errorf("unknown authz subcommand %q\n%s", args[0], authzUsage)}
	}
}

func withAuthzDB(ctx context.Context, path string, fn func(*sql.DB, *authzcontext.SQLStore) error) error {
	// Reject missing paths and directories before opening the store.
	if info, err := os.Stat(path); err != nil {
		return exitCodeError{code: 2, err: fmt.Errorf("open --db %q: %w", path, err)}
	} else if info.IsDir() {
		return exitCodeError{code: 2, err: fmt.Errorf("open --db %q: is a directory", path)}
	}
	sqlDB, err := db.OpenReadOnly(ctx, path)
	if err != nil {
		return exitCodeError{code: 2, err: fmt.Errorf("open authorization store: %w", err)}
	}
	defer func() { _ = sqlDB.Close() }()
	return fn(sqlDB, authzcontext.NewSQLStore(sqlDB))
}

func runAuthzVerify(args []string) error {
	fs := flag.NewFlagSet("authz verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to the sidecar store.db (required)")
	if err := parseStrictFlags(fs, args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	if strings.TrimSpace(*dbPath) == "" {
		return exitCodeError{code: 2, err: fmt.Errorf("--db is required\n%s", authzUsage)}
	}
	return withAuthzDB(context.Background(), *dbPath, func(sqlDB *sql.DB, _ *authzcontext.SQLStore) error {
		br, err := authzcontext.Verify(context.Background(), sqlDB)
		if err != nil {
			return exitCodeError{code: 2, err: fmt.Errorf("verify authorization evidence: %w", err)}
		}
		if br != nil {
			if err := json.NewEncoder(os.Stdout).Encode(br); err != nil {
				return exitCodeError{code: 2, err: fmt.Errorf("write verification finding: %w", err)}
			}
			return exitCodeError{code: 1, err: fmt.Errorf("authorization evidence verification failed")}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"ok": true})
	})
}

func runAuthzEvents(args []string) error {
	fs := flag.NewFlagSet("authz events", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to the sidecar store.db (required)")
	sessionID := fs.String("session", "", "session identifier (required)")
	limit := fs.Int("limit", authzcontext.DefaultListEventsLimit, "maximum events")
	if err := parseStrictFlags(fs, args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	if strings.TrimSpace(*dbPath) == "" || strings.TrimSpace(*sessionID) == "" {
		return exitCodeError{code: 2, err: fmt.Errorf("--db and --session are required\n%s", authzUsage)}
	}
	return withAuthzDB(context.Background(), *dbPath, func(_ *sql.DB, store *authzcontext.SQLStore) error {
		events, err := authzcontext.ListEvents(context.Background(), store, *sessionID, *limit)
		if err != nil {
			return exitCodeError{code: 2, err: fmt.Errorf("list authorization events: %w", err)}
		}
		return json.NewEncoder(os.Stdout).Encode(events)
	})
}

func runAuthzExport(args []string) error {
	fs := flag.NewFlagSet("authz export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to the sidecar store.db (required)")
	sessionID := fs.String("session", "", "session identifier (required)")
	if err := parseStrictFlags(fs, args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	if strings.TrimSpace(*dbPath) == "" || strings.TrimSpace(*sessionID) == "" {
		return exitCodeError{code: 2, err: fmt.Errorf("--db and --session are required\n%s", authzUsage)}
	}
	return withAuthzDB(context.Background(), *dbPath, func(_ *sql.DB, store *authzcontext.SQLStore) error {
		if err := authzcontext.ExportSessionAuthz(context.Background(), os.Stdout, store, *sessionID); err != nil {
			return exitCodeError{code: 2, err: fmt.Errorf("export authorization evidence: %w", err)}
		}
		return nil
	})
}

func runAuthzPersonActions(args []string) error {
	fs := flag.NewFlagSet("authz person-actions", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to the sidecar store.db (required)")
	limit := fs.Int("limit", authzcontext.DefaultListEventsLimit, "maximum actions")
	if err := parseStrictFlags(fs, args); err != nil {
		return exitCodeError{code: 2, err: err}
	}
	if strings.TrimSpace(*dbPath) == "" || *limit < 1 {
		return exitCodeError{code: 2, err: fmt.Errorf("--db is required and --limit must be positive\n%s", authzUsage)}
	}
	return withAuthzDB(context.Background(), *dbPath, func(sqlDB *sql.DB, _ *authzcontext.SQLStore) error {
		actions, err := personactions.New(sqlDB).Recent(context.Background(), *limit)
		if err != nil {
			return exitCodeError{code: 2, err: fmt.Errorf("list person actions: %w", err)}
		}
		return json.NewEncoder(os.Stdout).Encode(actions)
	})
}
