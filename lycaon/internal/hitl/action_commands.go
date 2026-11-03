package hitl

import "github.com/lycaon/lycaon/internal/argv"

// BoundCommands projects reached execution argv for authored command rules and
// screened approval presentation. Display strings never supply command identity.
func (in ActionInvocation) BoundCommands() []string {
	if in.BoundStages == nil {
		return nil
	}
	commands := make([]string, 0, len(in.BoundStages))
	for _, stage := range in.BoundStages {
		if len(stage.Argv) == 0 || stage.Argv[0] == "" {
			return nil
		}
		commands = append(commands, argv.JoinCommandLine(nil, stage.Argv[0], stage.Argv[1:]))
	}
	return commands
}
