package detectionpack

import (
	"path/filepath"
	"strings"
	"unicode"
)

// transparentWrappers, credentialBrokers, and packageRunners are peeled only during image normalization.
var transparentWrappers = map[string]struct{}{
	"sudo": {}, "env": {}, "command": {}, "exec": {}, "nohup": {},
	"time": {}, "stdbuf": {}, "nice": {}, "xargs": {},
}

var credentialBrokers = map[string]struct{}{
	"aws-vault": {}, "doppler": {}, "op": {}, "assume": {}, "chamber": {},
}

var packageRunners = map[string]struct{}{
	"npx": {}, "bunx": {}, "pnpx": {}, "dlx": {}, "pipx": {}, "uv": {},
	"poetry": {}, "rye": {}, "hatch": {}, "bundle": {}, "mise": {},
	"asdf": {}, "rbenv": {}, "pyenv": {},
	"npm": {}, "pnpm": {}, "yarn": {},
}

// alwaysPeelRunners always wrap another program (npx wrangler …). The rest only
// peel when a launcher subcommand follows (poetry run …, pnpm dlx …), so
// `poetry publish` and `npm install` keep their own Image.
var alwaysPeelRunners = map[string]struct{}{
	"npx": {}, "bunx": {}, "pnpx": {}, "dlx": {}, "pipx": {},
}

// packageRunnerPeelSub reports whether sub is a launcher verb for runner.
// npm/pnpm/yarn "run" names a package.json script, not a binary — only exec/dlx peel.
func packageRunnerPeelSub(runner, sub string) bool {
	switch runner {
	case "npm", "pnpm", "yarn":
		return sub == "exec" || sub == "dlx"
	default:
		return sub == "run" || sub == "exec" || sub == "dlx"
	}
}

type resolvedStage struct {
	image string
	argv  []string // tokens after resolution, argv[0] is the program basename when present
}

func resolveStages(raw string) []resolvedStage {
	normalized := strings.Join(strings.Fields(raw), " ")
	if normalized == "" {
		return nil
	}
	stageToks := splitStages(normalized)
	out := make([]resolvedStage, 0, len(stageToks))
	for _, toks := range stageToks {
		st := resolveOneStage(toks)
		if st.image != "" || len(st.argv) > 0 {
			out = append(out, st)
		}
	}
	return out
}

func resolveOneStage(tokens []string) resolvedStage {
	decoded := make([]string, len(tokens))
	for index, token := range tokens {
		decoded[index] = stripQuotes(token)
	}
	return resolveBoundStage(dropLeadingAssignments(decoded))
}

func resolveBoundStage(tokens []string) resolvedStage {
	toks := append([]string(nil), tokens...)
	for step := 0; step < 8; step++ {
		changed := false
		for len(toks) > 0 {
			base := strings.ToLower(filepath.Base(toks[0]))
			if _, ok := transparentWrappers[base]; ok {
				toks = peelTransparentWrapper(base, toks[1:])
				changed = true
				continue
			}
			break
		}
		if len(toks) == 0 {
			return resolvedStage{}
		}
		// Drop a bare -- a wrapper left behind.
		if toks[0] == "--" {
			toks = toks[1:]
			changed = true
			if len(toks) == 0 {
				return resolvedStage{}
			}
		}
		base := strings.ToLower(filepath.Base(toks[0]))
		if _, ok := credentialBrokers[base]; ok {
			if idx := indexBareDoubleDash(toks[1:]); idx >= 0 {
				toks = toks[1+idx+1:]
				continue
			}
		}
		if _, ok := packageRunners[base]; ok {
			rest := toks[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
				rest = rest[1:]
			}
			hasLauncherSub := false
			if len(rest) > 0 {
				sub := strings.ToLower(rest[0])
				hasLauncherSub = packageRunnerPeelSub(base, sub)
			}
			_, always := alwaysPeelRunners[base]
			if !always && !hasLauncherSub {
				// e.g. poetry publish / npm install — the runner is the program.
				break
			}
			toks = rest
			if hasLauncherSub {
				toks = toks[1:]
			}
			if len(toks) == 0 {
				return resolvedStage{}
			}
			continue
		}
		if !changed {
			break
		}
	}
	if len(toks) == 0 {
		return resolvedStage{}
	}
	argv := make([]string, len(toks))
	for i, t := range toks {
		argv[i] = t
	}
	image := strings.ToLower(filepath.Base(argv[0]))
	argv[0] = image
	return resolvedStage{image: image, argv: argv}
}

var wrapperOptionsWithValue = map[string]map[string]struct{}{
	"sudo": {"-C": {}, "-D": {}, "-g": {}, "-h": {}, "-p": {}, "-R": {}, "-T": {}, "-u": {},
		"--chdir": {}, "--close-from": {}, "--group": {}, "--host": {}, "--prompt": {},
		"--role": {}, "--type": {}, "--user": {}},
	"env":    {"-C": {}, "-S": {}, "-u": {}, "--chdir": {}, "--split-string": {}, "--unset": {}},
	"exec":   {"-a": {}},
	"nice":   {"-n": {}, "--adjustment": {}},
	"stdbuf": {"-i": {}, "-o": {}, "-e": {}},
	"time":   {"-f": {}, "-o": {}, "--format": {}, "--output": {}},
	"xargs": {"-a": {}, "-d": {}, "-E": {}, "-I": {}, "-L": {}, "-n": {}, "-P": {}, "-s": {},
		"--arg-file": {}, "--delimiter": {}, "--eof": {}, "--max-args": {}, "--max-chars": {},
		"--max-lines": {}, "--max-procs": {}, "--replace": {}},
}

func peelTransparentWrapper(wrapper string, args []string) []string {
	wantsValue := wrapperOptionsWithValue[wrapper]
	for len(args) > 0 {
		tok := args[0]
		if tok == "--" {
			return args[1:]
		}
		if (wrapper == "env" || wrapper == "sudo") && isEnvAssignment(tok) {
			args = args[1:]
			continue
		}
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			return args
		}
		if i := strings.IndexByte(tok, '='); i >= 0 {
			args = args[1:]
			continue
		}
		if _, ok := wantsValue[tok]; ok && len(args) > 1 {
			args = args[2:]
			continue
		}
		args = args[1:]
	}
	return nil
}

func dropLeadingAssignments(toks []string) []string {
	for len(toks) > 0 {
		t := toks[0]
		if isEnvAssignment(t) {
			toks = toks[1:]
			continue
		}
		break
	}
	return toks
}

func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	name := tok[:eq]
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !(unicode.IsLetter(r) || r == '_') {
				return false
			}
			continue
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}

func indexBareDoubleDash(toks []string) int {
	for i, t := range toks {
		if t == "--" {
			return i
		}
	}
	return -1
}
