package toolpresentation

import (
	"strconv"
	"strings"
)

// SubjectTitle combines a screened owner-resolved target with the requested action.
func SubjectTitle(tool string, args map[string]any, subject string) string {
	tool = strings.ToLower(tool)
	subject = argLine(subject)
	detail := Title(tool, args)
	if subject == "" {
		return detail
	}
	if tool == "answer_decision" || detail == titleFallbacks[tool] || detail == subject {
		return shortLine(subject)
	}
	if detail == "" {
		return subject
	}
	return boundedPair(subject, " · ", detail)
}

func listTitle(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	if len(labels) == 1 {
		return labels[0]
	}
	suffix := " +" + strconv.Itoa(len(labels)-1) + " more"
	return boundedPart(labels[0], 96-len([]rune(suffix))) + suffix
}

func boundedPart(text string, limit int) string {
	chars := []rune(text)
	if len(chars) <= limit {
		return text
	}
	return string(chars[:limit-1]) + "…"
}

func comparisonTitle(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return boundedPair(left, " → ", right)
}

func compositeTitle(tool string, args map[string]any) (string, bool) {
	switch tool {
	case "diff":
		return comparisonTitle(argLine(args["path_a"]), argLine(args["path_b"])), true
	case "git_compare":
		return comparisonTitle(argLine(args["base_ref"]), argLine(args["head_ref"])), true
	case "command", "verify":
		if command := argLine(args["command"]); command != "" {
			return command, true
		}
		var stages []string
		switch value := args["pipeline"].(type) {
		case []string:
			for _, entry := range value {
				if text := argLine(entry); text != "" {
					stages = append(stages, text)
				}
			}
		case []any:
			for _, entry := range value {
				if text := argLine(entry); text != "" {
					stages = append(stages, text)
				}
			}
		}
		if len(stages) < 2 {
			return strings.Join(stages, ""), true
		}
		suffix := ""
		if len(stages) > 2 {
			unit := "stages"
			if len(stages) == 3 {
				unit = "stage"
			}
			suffix = " +" + strconv.Itoa(len(stages)-2) + " " + unit
		}
		return pairWithin(stages[0], " | ", stages[1], 96-len(suffix)) + suffix, true
	}
	return "", false
}

func boundedPair(left, separator, right string) string {
	return pairWithin(left, separator, right, 96)
}

func pairWithin(left, separator, right string, limit int) string {
	available := limit - len([]rune(separator))
	leftLimit := available / 2
	rightLimit := available - leftLimit
	if n := len([]rune(left)); n < leftLimit {
		rightLimit += leftLimit - n
	}
	if n := len([]rune(right)); n < rightLimit {
		leftLimit = available - n
	}
	return boundedPart(left, leftLimit) + separator + boundedPart(right, rightLimit)
}
