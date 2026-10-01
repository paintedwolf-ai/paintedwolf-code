package settings

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
)

// MatchCommandPattern reports whether command matches an authored deny/ask
// pattern (head/tail wildcards on the full structured command argument).
func MatchCommandPattern(command, pattern string) bool {
	command = strings.TrimSpace(command)
	pattern = strings.TrimSpace(pattern)
	if command == "" || pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	normalized := strings.ReplaceAll(command, `\`, "/")
	// Separators fold first, so no backslash survives into the escape switch.
	escaped := strings.ReplaceAll(pattern, `\`, "/")
	var b strings.Builder
	for _, r := range escaped {
		switch r {
		case '.', '+', '^', '$', '{', '}', '(', ')', '|', '[', ']':
			b.WriteRune('\\')
			b.WriteRune(r)
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteRune('.')
		default:
			b.WriteRune(r)
		}
	}
	escaped = b.String()
	if strings.HasSuffix(escaped, " .*") {
		escaped = escaped[:len(escaped)-3] + "( .*)?"
	}
	re, err := regexp.Compile("(?s)^" + escaped + "$")
	if err != nil {
		return false
	}
	return re.MatchString(normalized)
}

// CommandTextFromActionArgs extracts the command line from tool arguments.
func CommandTextFromActionArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	cmd, _ := args["command"].(string)
	return strings.TrimSpace(cmd)
}

// commandUnitsFromArgs returns the composition and each executable stage.
func commandUnitsFromArgs(args map[string]any) []string {
	if args == nil {
		return nil
	}
	var units []string
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		for _, existing := range units {
			if existing == text {
				return
			}
		}
		units = append(units, text)
	}

	if cmd := CommandTextFromActionArgs(args); cmd != "" {
		add(cmd)
		// Rejected command syntax has no executable stages.
		if elements, err := argv.SplitSequence(cmd); err == nil {
			for _, el := range elements {
				add(argv.JoinCommandLine(el.Env, el.Name, el.Args))
			}
		}
	}

	stages := pipelineStageTexts(args)
	if len(stages) > 0 {
		add(strings.Join(stages, " | "))
		for _, stage := range stages {
			add(stage)
		}
	}
	return units
}

// pipelineStageTexts normalizes each accepted pipeline shape.
func pipelineStageTexts(args map[string]any) []string {
	var raw []any
	switch v := args["pipeline"].(type) {
	case []any:
		raw = v
	case []string:
		for _, s := range v {
			raw = append(raw, s)
		}
	default:
		return nil
	}
	var stages []string
	for _, item := range raw {
		line, ok := item.(string)
		if !ok {
			continue
		}
		env, name, cmdArgs, err := argv.SplitCommandLine(line)
		if err != nil {
			// Preserve unparseable text for exact deny rules.
			stages = append(stages, strings.TrimSpace(line))
			continue
		}
		stages = append(stages, argv.JoinCommandLine(env, name, cmdArgs))
	}
	return stages
}
