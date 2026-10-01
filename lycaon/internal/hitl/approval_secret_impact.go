package hitl

import (
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// SecretImpact describes the receiver and protection status.
func SecretImpact(screen *SecretScreen) string {
	if screen == nil {
		return ""
	}
	if len(screen.Recipients) > 1 {
		labels := make([]string, 0, len(screen.Recipients))
		process := false
		for _, recipient := range screen.Recipients {
			labels = append(labels, recipient.Label)
			process = process || recipient.Kind == secretmatch.DestinationProcess
		}
		line := "Allow the protected values to be used with " + strings.Join(labels, "; ") + "."
		if process {
			line += " Processes and daemons may retain or forward the values; their onward use is not observed."
		}
		return line
	}
	subject := "this value"
	if screen.Managed {
		subject = "this protected value"
	}
	if screen.RuleID == secretmatch.UnscreenedRuleID {
		subject = "this image, including any text the host could not check"
	}
	destination := strings.TrimSpace(screen.DestinationLabel)
	if destination == "" {
		destination = strings.TrimSpace(screen.DestinationID)
	}
	switch screen.DestinationKind {
	case secretmatch.DestinationModelProvider:
		if destination == "" {
			return "The model would read " + subject + " in this request."
		}
		return "The model at " + destination + " would read " + subject + " in this request."
	case secretmatch.DestinationProcess:
		return upperFirst(subject) + " is handed to this process. Where that process sends it" +
			" from there is not observed here."
	default:
		surface := strings.TrimSpace(screen.SurfaceLabel)
		if surface == "" {
			surface = "request"
		}
		if destination == "" {
			return "This " + surface + " would send " + subject + " out of this machine."
		}
		return "This " + surface + " would send " + subject + " to " + destination + "."
	}
}

func upperFirst(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
