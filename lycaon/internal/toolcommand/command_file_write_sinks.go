package toolcommand

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
)

func isPassThroughProducer(st bashStage) (passThrough, exact bool) {
	if len(st.argv) == 0 {
		return false, false
	}
	if filepath.Base(st.argv[0].value) != "cat" || !st.argv[0].literal {
		return false, false
	}
	return parseCatStdinArgs(st.argv[1:])
}

func isPassThroughWriterStage(st bashStage, hasOutRedirect bool) (passThrough, exact bool) {
	if len(st.argv) == 0 {
		return false, false
	}
	prog := filepath.Base(st.argv[0].value)
	if !st.argv[0].literal {
		return false, false
	}
	switch prog {
	case "cat":
		if !hasOutRedirect {
			return false, false
		}
		return parseCatStdinArgs(st.argv[1:])
	case "tee":
		_, _, _, teeExact, teeOK := parseTeeArgs(st.argv[1:])
		return teeOK, teeOK && teeExact && !hasOutRedirect
	case "dd":
		_, _, _, ddExact, ddOK := parseDDArgs(st.argv[1:])
		return ddOK, ddOK && ddExact && !hasOutRedirect
	default:
		return false, false
	}
}

func parseCatStdinArgs(args []bashWord) (passThrough, exact bool) {
	optionsDone := false
	for _, a := range args {
		if !a.literal {
			return false, false
		}
		if !optionsDone && a.value == "--" {
			optionsDone = true
			continue
		}
		if a.value == "-" || (!optionsDone && a.value == "-u") {
			continue
		}
		return false, false
	}
	return true, true
}

func parseTeeArgs(args []bashWord) (path string, literal, appendMode, exact, ok bool) {
	optionsDone := false
	var paths []bashWord
	exact = true
	for _, a := range args {
		if !optionsDone && a.value == "--" {
			optionsDone = true
			continue
		}
		if !optionsDone && strings.HasPrefix(a.value, "-") && a.value != "-" {
			switch a.value {
			case "-a", "--append":
				appendMode = true
			case "-i", "--ignore-interrupts":
			default:
				if strings.TrimLeft(strings.TrimPrefix(a.value, "-"), "ai") == "" {
					if strings.Contains(a.value, "a") {
						appendMode = true
					}
					continue
				}
				exact = false
			}
			continue
		}
		if isStdoutFileTarget(a.value) {
			paths = append(paths, a)
		}
	}
	if len(paths) == 0 {
		return "", false, false, false, false
	}
	if len(paths) > 1 {
		exact = false
	}
	return paths[0].value, paths[0].literal, appendMode, exact, true
}

func parseDDArgs(args []bashWord) (path string, literal, appendMode, exact, ok bool) {
	exact = true
	for _, a := range args {
		switch {
		case strings.HasPrefix(a.value, "of="):
			target := strings.TrimPrefix(a.value, "of=")
			if isStdoutFileTarget(target) {
				path = target
				literal = a.literal
				ok = true
			}
		case strings.HasPrefix(a.value, "status="):
		case a.value == "conv=notrunc":
			exact = false
		case a.value == "oflag=append":
			appendMode = true
			exact = false
		case strings.HasPrefix(a.value, "if="):
			return "", false, false, false, false
		default:
			exact = false
		}
	}
	return path, literal, appendMode, exact, ok
}

func unwrapShellCommand(st bashStage) (string, bool) {
	argv := st.argv
	if len(argv) == 0 {
		return "", false
	}
	if filepath.Base(argv[0].value) == "env" {
		argv = argv[1:]
		for len(argv) > 0 && (isEnvAssignment(argv[0]) || strings.HasPrefix(argv[0].value, "-")) {
			argv = argv[1:]
		}
	}
	if !isShellProgram(argv) {
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		val := argv[i].value
		if val == "--" {
			break
		}
		if val == "-c" || (strings.HasPrefix(val, "-") && !strings.HasPrefix(val, "--") && strings.HasSuffix(val, "c") && strings.TrimLeft(val[1:len(val)-1], "elxufv") == "") {
			if i+1 < len(argv) {
				return argv[i+1].value, true
			}
			return "", false
		}
	}
	return "", false
}

func isShellProgram(argv []bashWord) bool {
	if len(argv) == 0 || !argv[0].literal {
		return false
	}
	switch filepath.Base(argv[0].value) {
	case "sh", "bash", "zsh", "dash", "ksh":
		return true
	default:
		return false
	}
}

func literalEchoOrPrintf(argv []bashWord) (body string, literal, ok bool) {
	if len(argv) == 0 || !argv[0].literal {
		return "", false, false
	}
	prog := filepath.Base(argv[0].value)
	switch prog {
	case "echo":
		newline := true
		interpretEscapes := false
		idx := 1
		for idx < len(argv) && strings.HasPrefix(argv[idx].value, "-") && len(argv[idx].value) > 1 {
			flags := argv[idx].value[1:]
			if strings.TrimLeft(flags, "neE") != "" {
				break
			}
			if strings.ContainsRune(flags, 'n') {
				newline = false
			}
			if strings.ContainsRune(flags, 'e') {
				interpretEscapes = true
			}
			if strings.ContainsRune(flags, 'E') {
				interpretEscapes = false
			}
			idx++
		}
		var parts []string
		// Echo option and backslash behavior differs between external programs and shells.
		lit := !interpretEscapes && idx == 1 && (len(argv) < 2 || !strings.HasPrefix(argv[1].value, "-"))
		for ; idx < len(argv); idx++ {
			if !argv[idx].literal || strings.Contains(argv[idx].value, `\`) {
				lit = false
			}
			parts = append(parts, argv[idx].value)
		}
		out := strings.Join(parts, " ")
		if newline {
			out += "\n"
		}
		return out, lit, true
	case "printf":
		if len(argv) < 2 {
			return "", false, false
		}
		if argv[1].value == "%s" && len(argv) == 3 {
			return argv[2].value, argv[1].literal && argv[2].literal, true
		}
		if argv[1].value == "%s\\n" && len(argv) == 3 {
			return argv[2].value + "\n", argv[1].literal && argv[2].literal, true
		}
		return "", false, true
	default:
		return "", false, false
	}
}

// Direct commands use the host argv grammar; wrappers use shell quote removal.
// Only claim byte equivalence when both interpretations agree, including paths.
func literalOutputArgvExact(st bashStage) bool {
	words := append([]bashWord(nil), st.argv...)
	for _, redirect := range st.redirects {
		words = append(words, redirect.target)
	}
	raw := []string{"echo"}
	for _, word := range words {
		raw = append(raw, word.raw)
	}
	parsed, err := argv.SplitElement(strings.Join(raw, " "))
	if err != nil || len(parsed.Args) != len(words) || len(parsed.Globs) != 0 || len(parsed.Addressed) != 0 {
		return false
	}
	for i, word := range words {
		if parsed.Args[i] != word.value {
			return false
		}
	}
	return true
}
