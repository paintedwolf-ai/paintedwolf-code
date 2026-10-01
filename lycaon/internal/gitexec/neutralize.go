package gitexec

import "strings"

// neutralizeArgs disables repository-owned execution hooks.
func neutralizeArgs(sshCommand string) ([]string, error) {
	hooksDir, err := emptyHooksDirFn()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(sshCommand) == "" {
		sshCommand = defaultSSHCommand
	}
	// Order controls override precedence.
	return []string{
		"-c", "core.hooksPath=" + hooksDir,
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "core.editor=false",
		"-c", "core.askpass=",
		"-c", "core.attributesFile=/dev/null",
		"-c", "core.excludesFile=/dev/null",
		// Empty SSH commands are executable, so use a trusted command.
		"-c", "core.sshCommand=" + sshCommand,
		// An empty value clears the multi-valued helper list.
		"-c", "credential.helper=",
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=user",
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		// Disable config-triggered verification programs.
		"-c", "log.showSignature=false",
		"-c", "merge.verifySignatures=false",
		"-c", "advice.detachedHead=false",
	}, nil
}

// Batch mode turns unavailable prompts into immediate errors.
const defaultSSHCommand = "ssh -o BatchMode=yes"

// neutralizePrefixKeys mirrors the override order for invariant checks.
var neutralizePrefixKeys = []string{
	"core.hooksPath",
	"core.fsmonitor",
	"core.pager",
	"core.editor",
	"core.askpass",
	"core.attributesFile",
	"core.excludesFile",
	"core.sshCommand",
	"credential.helper",
	"protocol.ext.allow",
	"protocol.file.allow",
	"commit.gpgSign",
	"tag.gpgSign",
	"log.showSignature",
	"merge.verifySignatures",
	"advice.detachedHead",
}

// Empty config values still execute drivers, so command flags disable them.
var diffDriverFlags = []string{"--no-ext-diff", "--no-textconv"}

// diffDriverCommands lists commands that evaluate diff drivers.
var diffDriverCommands = map[string]bool{
	"blame":        true,
	"diff":         true,
	"diff-files":   true,
	"diff-index":   true,
	"diff-tree":    true,
	"format-patch": true,
	"log":          true,
	"show":         true,
	"whatchanged":  true,
}

// diffDriverSubcommands records subcommand-specific flag placement.
var diffDriverSubcommands = map[string]map[string]bool{
	"stash": {"show": true},
}

// diffDriverFlagArgs inserts missing flags at their valid command position.
func diffDriverFlagArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	at := -1
	switch {
	case diffDriverCommands[args[0]]:
		at = 1
	case diffDriverSubcommands[args[0]] != nil && len(args) > 1 && diffDriverSubcommands[args[0]][args[1]]:
		at = 2
	}
	if at < 0 {
		return args
	}
	present := map[string]bool{}
	for _, a := range args[at:] {
		if a == "--" {
			break
		}
		present[a] = true
	}
	missing := make([]string, 0, len(diffDriverFlags))
	for _, flag := range diffDriverFlags {
		if !present[flag] {
			missing = append(missing, flag)
		}
	}
	if len(missing) == 0 {
		return args
	}
	out := make([]string, 0, len(args)+len(missing))
	out = append(out, args[:at]...)
	out = append(out, missing...)
	return append(out, args[at:]...)
}
