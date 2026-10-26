package exec

import "strings"

// agentProcessDefaults keep noninteractive launches from opening an editor or
// credential dialog. Both native environment inspection and child launches use
// these values through ProcessEnvironment.
var agentProcessDefaults = []string{
	"GIT_PAGER=cat",
	"GIT_EDITOR=false",
	"GIT_SEQUENCE_EDITOR=false",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_ASKPASS=false",
	"GCM_INTERACTIVE=never",
	"SSH_ASKPASS=false",
	"SSH_ASKPASS_REQUIRE=never",
}

func isAgentProcessDefault(name, value string) bool {
	for _, entry := range agentProcessDefaults {
		key, defaultValue, _ := strings.Cut(entry, "=")
		if name == key && value == defaultValue {
			return true
		}
	}
	return false
}
