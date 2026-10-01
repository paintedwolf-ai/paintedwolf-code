package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/credentialstore"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/webresearch"
)

const recoveryUsage = `usage: pw credentials {where|purge|verify-protection} [--yes]

Inspect and reset every private credential namespace:
  where   name the backend holding each credential set
  purge   remove every stored credential; requires --yes
  verify-protection   verify native Keychain protection using disposable identities

Purging is refused while an engine is running, because a running engine holds
the decrypted identity and credential values in memory until it exits.

Release builds keep all credential values in one age-encrypted vault. macOS
keeps only the vault identity in Keychain. Linux and Windows wrap that identity
with the app password. Development builds use a private local identity file.`

func runCredentials(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", recoveryUsage)
	}
	if args[0] == "verify-protection" {
		if len(args) != 1 {
			return fmt.Errorf("verify-protection takes no arguments")
		}
		if err := credentialstore.VerifyIdentityProtection(); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "native credential identity protection verified")
		return nil
	}
	slots, err := allCredentialSlots()
	if err != nil {
		return err
	}
	switch args[0] {
	case "where":
		return describeCredentials(slots)
	case "purge":
		return purgeCredentials(args[1:], slots)
	default:
		return fmt.Errorf("unknown credentials command %q\n%s", args[0], recoveryUsage)
	}
}

func allCredentialSlots() ([]credentialstore.Slot, error) {
	provider, err := providercredentials.DefaultSlot()
	if err != nil {
		return nil, err
	}
	research, err := webresearch.DefaultSlot()
	if err != nil {
		return nil, err
	}
	oauth, err := mcp.DefaultSlot()
	if err != nil {
		return nil, err
	}
	fingerprint, err := secretmatch.DefaultFingerprintSlot()
	if err != nil {
		return nil, err
	}
	managed, err := secretcap.DefaultSlot()
	if err != nil {
		return nil, err
	}
	return []credentialstore.Slot{provider, research, oauth, fingerprint, managed}, nil
}

func describeCredentials(slots []credentialstore.Slot) error {
	for _, slot := range slots {
		where, err := credentialstore.DescribeSlot(slot)
		if err != nil {
			return err
		}
		fmt.Printf("%-14s %s\n", slot.Context+":", where)
	}
	return nil
}

// runningEnginePID prevents vault reset while credentials remain in memory.
func runningEnginePID() int {
	manifest, err := api.ReadDaemonManifest()
	if err != nil || manifest.PID == 0 || !osprocess.Alive(manifest.PID) {
		return 0
	}
	return manifest.PID
}

func purgeCredentials(args []string, slots []credentialstore.Slot) error {
	fs := flag.NewFlagSet("credentials purge", flag.ContinueOnError)
	confirm := fs.Bool("yes", false, "confirm removal of every stored credential")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return fmt.Errorf("refusing to purge credentials without --yes")
	}
	if pid := runningEnginePID(); pid != 0 {
		return fmt.Errorf(
			"an engine is running (pid %d) and holds these credentials in memory; "+
				"quit the app before resetting the credential vault", pid)
	}
	if len(slots) == 0 {
		return nil
	}
	if err := credentialstore.PurgeVault(slots[0].Path); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "purged encrypted credential vault and identity")
	return nil
}
