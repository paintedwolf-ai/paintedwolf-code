package gitcmd

import (
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

const allowedConfigExpected = "an allowlisted -c override (user.name, user.email, init.defaultBranch, core.fsmonitor=false, core.pager=cat, color.ui=false, core.quotepath=false)"

// validateConfigOverrides enforces the -c allowlist as a terminal policy
// refusal that never falls back to an external git binary.
func validateConfigOverrides(commandName, expectedForm string, configs []string) error {
	for _, kv := range configs {
		key, value, _ := strings.Cut(kv, "=")
		if allowed := allowedConfig[strings.ToLower(key)]; allowed == nil || !allowed(value) {
			return &toolrejection.ToolReject{
				NoFallback: true,
				Code:       "COMMAND_OPTION_UNSUPPORTED",
				Data: map[string]any{
					"command":       commandName,
					"word":          kv,
					"expected":      allowedConfigExpected,
					"expected_form": expectedForm,
				},
			}
		}
	}
	return nil
}

// ValidateGlobalConfig checks leading global -c KEY=VALUE overrides on a git
// invocation before subcommand dispatch or native-gap fallback.
func ValidateGlobalConfig(argv commandprogram.Fields) error {
	if len(argv) < 2 || argv[0].Value != "git" {
		return nil
	}
	var configs []string
	for i := 1; i < len(argv); {
		word := argv[i].Value
		switch {
		case word == "-C":
			i += 2
		case word == "-c":
			if i+1 < len(argv) {
				configs = append(configs, argv[i+1].Value)
			}
			i += 2
		case strings.HasPrefix(word, "-c"):
			configs = append(configs, word[2:])
			i++
		case strings.HasPrefix(word, "-C") || word == "--no-pager":
			i++
		default:
			return validateConfigOverrides("git", "", configs)
		}
	}
	return validateConfigOverrides("git", "", configs)
}
