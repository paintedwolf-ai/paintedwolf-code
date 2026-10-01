package guidance

import "strings"

// pipedTUIControlMarkers are the alternate-screen escape sequences full-screen
// programs emit. In command/verify pipe output they mean the agent ran a TUI
// without a controlling terminal; the banner steers to terminal_*.
var pipedTUIControlMarkers = []string{
	"\x1b[?1049h", // xterm alternate screen buffer
	"\x1b[?1047h", // DEC alternate screen
	"\x1b[?47h",   // classic alternate screen
}

// outputLooksLikePipedTUI reports whether pipe-path tool output contains an
// alternate-screen control sequence.
func outputLooksLikePipedTUI(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}
	for _, marker := range pipedTUIControlMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}
