package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/api"
)

const tokenUsage = "usage: pw token print"

// runToken implements the strictly read-only CLI retrieval command for the
// standalone daemon bearer token. It honors LYCAON_API_TOKEN precedence and
// otherwise reads ("daemon-token", "bearer") from the active profile's
// credential-vault.age without creating or rotating any token.
func runToken(args []string, out io.Writer) error {
	if len(args) != 1 || args[0] != "print" {
		return errors.New(tokenUsage)
	}
	if envToken := strings.TrimSpace(os.Getenv("LYCAON_API_TOKEN")); envToken != "" {
		_, err := fmt.Fprintln(out, envToken)
		return err
	}
	token, err := api.ReadAPIToken()
	if err != nil {
		return fmt.Errorf("read daemon token: %w", err)
	}
	_, err = fmt.Fprintln(out, token)
	return err
}
