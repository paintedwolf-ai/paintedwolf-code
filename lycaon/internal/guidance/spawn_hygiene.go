package guidance

import "strings"

// StripHostBlocks removes host-emitted guidance blocks from a prompt: the markers
// this package writes, and the code line naming a spec or coordinator code.
// Prose that reads like an instruction is left alone — the host does not classify
// text it did not write.
func StripHostBlocks(prompt string) string {
	lines := strings.Split(prompt, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if isHostBlockLine(trim) {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isHostBlockLine(trim string) bool {
	for _, marker := range []string{markerSpecPosture, markerToolFeedback, markerRequiredNext} {
		if strings.Contains(trim, marker) {
			return true
		}
	}
	if strings.HasPrefix(trim, markerRejected) {
		return true
	}
	if !strings.HasPrefix(trim, codeLinePrefix) {
		return false
	}
	code := strings.TrimSpace(strings.TrimPrefix(trim, codeLinePrefix))
	return codeFamilyIs(code, codeFamilySpec, codeFamilyCoordinator)
}
