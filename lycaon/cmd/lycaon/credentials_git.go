package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/gitcredential"
)

const gitCredentialsUsage = `usage: pw credentials git {list|set <remote> --username <name>|remove <remote>}

Git HTTPS credentials the host's own helper serves on Linux:
  list                       name each stored remote and its username
  set <remote> --username N  store the token read from the first line of stdin
  remove <remote>            forget the remote's credential

With LYCAON_CREDENTIAL_UNLOCK_STDIN set, the app password frame precedes the
token on stdin, as it does for the engine.`

// gitCredentialStore is the vault the CLI edits; tests replace it.
var gitCredentialStore = func() gitCredentialBackend { return gitcredential.VaultBackend() }

type gitCredentialBackend interface {
	List() ([]gitcredential.Entry, error)
	Store(gitcredential.Credential) error
	Erase(gitcredential.Credential) error
}

func runGitCredentials(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(gitCredentialsUsage)
	}
	store := gitCredentialStore()
	switch args[0] {
	case "list":
		entries, err := store.List()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if _, err := fmt.Fprintf(out, "%s\t%s\n", entry.Key, entry.Username); err != nil {
				return err
			}
		}
		return nil
	case "set":
		return setGitCredential(args[1:], store, in)
	case "remove":
		if len(args) != 2 {
			return errors.New(gitCredentialsUsage)
		}
		credential, err := gitcredential.CredentialForRemote(args[1])
		if err != nil {
			return err
		}
		return store.Erase(credential)
	default:
		return fmt.Errorf("unknown credentials git command %q\n%s", args[0], gitCredentialsUsage)
	}
}

func setGitCredential(args []string, store gitCredentialBackend, in io.Reader) error {
	fs := flag.NewFlagSet("credentials git set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	username := fs.String("username", "", "account name the token belongs to")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 || strings.TrimSpace(*username) == "" {
		return errors.New(gitCredentialsUsage)
	}
	credential, err := gitcredential.CredentialForRemote(fs.Arg(0))
	if err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	if os.Getenv(credentialstore.UnlockStdinEnv) != "" {
		if err := credentialstore.ReadUnlockFrame(reader); err != nil {
			return err
		}
	}
	token, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read token: %w", err)
	}
	token = strings.TrimRight(token, "\r\n")
	if token == "" {
		return errors.New("a token is required on the first line of stdin")
	}
	credential.Username = strings.TrimSpace(*username)
	credential.Password = token
	return store.Store(credential)
}
