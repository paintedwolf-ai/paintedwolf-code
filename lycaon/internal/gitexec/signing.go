package gitexec

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/exec"
)

// Signing authority comes from invocation fields and installed programs.
type signingSubject string

const (
	signingSubjectCommit signingSubject = "commits"
	signingSubjectTag    signingSubject = "tags"
)

// signingCommands maps commands to signed object types.
var signingCommands = map[string]signingSubject{
	"commit":      signingSubjectCommit,
	"commit-tree": signingSubjectCommit,
	"merge":       signingSubjectCommit,
	"rebase":      signingSubjectCommit,
	"cherry-pick": signingSubjectCommit,
	"revert":      signingSubjectCommit,
	"am":          signingSubjectCommit,
	"tag":         signingSubjectTag,
}

// Short signing flags have command-specific meanings.
var signingFlags = map[signingSubject]signRequestFlags{
	signingSubjectCommit: {
		exact:  []string{"-S", "--gpg-sign"},
		prefix: []string{"-S", "--gpg-sign="},
		off:    []string{"--no-gpg-sign"},
	},
	signingSubjectTag: {
		exact:  []string{"-s", "--sign", "-u"},
		prefix: []string{"-u", "--local-user="},
		off:    []string{"--no-sign"},
	},
}

// signingConfigKeys maps object types to their opt-in setting.
var signingConfigKeys = map[signingSubject]string{
	signingSubjectCommit: "commit.gpgsign",
	signingSubjectTag:    "tag.gpgsign",
}

type signRequestFlags struct {
	exact  []string
	prefix []string
	off    []string
}

// These global options consume the following token.
var gitGlobalOptionsWithValue = map[string]bool{
	"-c":           true,
	"-C":           true,
	"--exec-path":  true,
	"--git-dir":    true,
	"--work-tree":  true,
	"--namespace":  true,
	"--config-env": true,
}

func signingUnsupported(args []string, opts Opts) *SigningUnsupportedError {
	subject, ok := signingRequested(args, opts.ExtraConfig)
	if !ok {
		return nil
	}
	program, missing := unresolvedSigningProgram(opts.ExtraConfig)
	if !missing {
		return nil
	}
	return &SigningUnsupportedError{
		Detail: fmt.Sprintf("signed %s requested; signing program %q is not on PATH", subject, program),
	}
}

func signingRequested(args []string, extraConfig []string) (signingSubject, bool) {
	subject, rest, ok := signingSubcommand(args)
	if !ok {
		return "", false
	}
	if requested, explicit := signFlagRequest(subject, rest); explicit {
		return subject, requested
	}
	return subject, configEnablesSigning(subject, extraConfig)
}

func signingSubcommand(args []string) (signingSubject, []string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return "", nil, false
		}
		if gitGlobalOptionsWithValue[arg] {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		subject, ok := signingCommands[arg]
		if !ok {
			return "", nil, false
		}
		return subject, args[i+1:], true
	}
	return "", nil, false
}

// The last explicit signing flag wins.
func signFlagRequest(subject signingSubject, args []string) (requested, explicit bool) {
	flags := signingFlags[subject]
	for _, arg := range args {
		if arg == "--" {
			break
		}
		switch {
		case matchesAny(arg, flags.off, nil):
			requested, explicit = false, true
		case matchesAny(arg, flags.exact, flags.prefix):
			requested, explicit = true, true
		}
	}
	return requested, explicit
}

func matchesAny(arg string, exact, prefixes []string) bool {
	for _, e := range exact {
		if arg == e {
			return true
		}
	}
	for _, p := range prefixes {
		if len(arg) > len(p) && strings.HasPrefix(arg, p) {
			return true
		}
	}
	return false
}

func configEnablesSigning(subject signingSubject, extraConfig []string) bool {
	key := signingConfigKeys[subject]
	enabled := false
	for _, kv := range extraConfig {
		name, value, hasValue := strings.Cut(strings.TrimSpace(kv), "=")
		if !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		// A key without "=" means true.
		enabled = !hasValue || gitConfigTrue(value)
	}
	return enabled
}

func gitConfigTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

var signingProgramDefaults = map[string]string{
	"openpgp": "gpg",
	"ssh":     "ssh-keygen",
	"x509":    "gpgsm",
}

const defaultSigningFormat = "openpgp"

// unresolvedSigningProgram resolves the configured or format-default program.
func unresolvedSigningProgram(extraConfig []string) (string, bool) {
	format := strings.ToLower(strings.TrimSpace(configValue(extraConfig, "gpg.format")))
	if format == "" {
		format = defaultSigningFormat
	}
	program := strings.TrimSpace(configValue(extraConfig, "gpg."+format+".program"))
	if program == "" && format == defaultSigningFormat {
		// OpenPGP also accepts the unqualified setting.
		program = strings.TrimSpace(configValue(extraConfig, "gpg.program"))
	}
	if program == "" {
		known, ok := signingProgramDefaults[format]
		if !ok {
			return "gpg." + format + ".program", true
		}
		program = known
	}
	if _, err := exec.LookPath(program); err != nil {
		return program, true
	}
	return program, false
}

func configValue(extraConfig []string, key string) string {
	out := ""
	for _, kv := range extraConfig {
		name, value, hasValue := strings.Cut(strings.TrimSpace(kv), "=")
		if !hasValue || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		out = strings.TrimSpace(value)
	}
	return out
}
