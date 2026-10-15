package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/credentialstore"
)

// custodyMoves maps each move command to its operation.
var custodyMoves = map[string]func(string) error{
	"move-to-service":      credentialstore.MoveIdentityToService,
	"move-to-app-password": credentialstore.MoveIdentityToAppPassword,
}

func describeCustody(stdout io.Writer, vaultPath string) error {
	report, err := credentialstore.DescribeCustody(vaultPath)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}

// moveCustody runs one move with the app password framed on stdin when
// the desktop app supplies it, and prints the outcome as JSON.
func moveCustody(name string, args []string, stdin io.Reader, stdout io.Writer, vaultPath string, move func(string) error) error {
	fs := flag.NewFlagSet("credentials "+name, flag.ContinueOnError)
	confirm := fs.Bool("yes", false, "confirm moving the vault identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return fmt.Errorf("refusing to move the vault identity without --yes")
	}
	if pid := runningEnginePID(); pid != 0 {
		return fmt.Errorf("an engine is running (pid %d) and holds the vault identity; quit the app before moving it", pid)
	}
	if strings.TrimSpace(os.Getenv(credentialstore.UnlockStdinEnv)) != "" {
		if err := credentialstore.ReadUnlockFrame(stdin); err != nil {
			return err
		}
		defer credentialstore.ClearUnlockSecret()
	}
	moveErr := move(vaultPath)
	outcome := credentialstore.CustodyOutcome{Code: credentialstore.CustodyErrorCode(moveErr)}
	if moveErr != nil {
		outcome.Message = moveErr.Error()
	}
	if err := json.NewEncoder(stdout).Encode(outcome); err != nil {
		return err
	}
	return moveErr
}
