package hostcmd

import "strings"

// CommandLine preserves the execution plan's arguments and stage connectors.
func CommandLine(stages []StageResult) string {
	var out strings.Builder
	for _, stage := range stages {
		command := strings.TrimSpace(stage.Command)
		if command == "" {
			continue
		}
		if out.Len() > 0 {
			connector := stage.Connector
			if connector == "" {
				connector = "|"
			}
			out.WriteString(" " + connector + " ")
		}
		out.WriteString(command)
	}
	return out.String()
}
