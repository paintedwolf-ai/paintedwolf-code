package main

import (
	"bufio"
	"errors"
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

// setGitCredential accepts the remote and --username in either order.
func setGitCredential(args []string, store gitCredentialBackend, in io.Reader) error {
	remote, username := "", ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--username" && i+1 < len(args):
			i++
			username = strings.TrimSpace(args[i])
		case strings.HasPrefix(args[i], "--username="):
			username = strings.TrimSpace(strings.TrimPrefix(args[i], "--username="))
		case remote == "" && !strings.HasPrefix(args[i], "-"):
			remote = args[i]
		default:
			return errors.New(gitCredentialsUsage)
		}
	}
	if remote == "" || username == "" {
		return errors.New(gitCredentialsUsage)
	}
	credential, err := gitcredential.CredentialForRemote(remote)
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
	credential.Username = username
	credential.Password = token
	return store.Store(credential)
}
