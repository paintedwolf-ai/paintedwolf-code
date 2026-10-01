package native

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

var allowedChmodPerm = map[uint32]struct{}{
	0o755: {},
	0o750: {},
	0o744: {},
	0o700: {},
	0o644: {},
	0o640: {},
	0o600: {},
	0o664: {},
}

func resolveChmodMode(current os.FileMode, spec string) (os.FileMode, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, toolkit.MissingArg("mode")
	}
	if strings.ContainsAny(spec, "sStT") {
		return 0, &tools.ToolReject{
			Code: "CHMOD_SPECIAL_BIT_DENIED",
			Data: map[string]any{"mode": spec},
		}
	}
	if isOctalModeSpec(spec) {
		return parseOctalChmodMode(spec)
	}
	return applySymbolicChmodMode(current, spec)
}

func isOctalModeSpec(spec string) bool {
	if spec == "" {
		return false
	}
	for _, r := range spec {
		if r < '0' || r > '7' {
			return false
		}
	}
	return len(spec) == 3 || len(spec) == 4
}

func parseOctalChmodMode(spec string) (os.FileMode, error) {
	if !isOctalModeSpec(spec) {
		return 0, chmodModeDenied(spec)
	}
	if len(spec) == 4 {
		var special uint32
		if _, err := fmt.Sscanf(spec, "%o", &special); err != nil {
			return 0, chmodModeDenied(spec)
		}
		if special&0o7000 != 0 {
			return 0, &tools.ToolReject{
				Code: "CHMOD_SPECIAL_BIT_DENIED",
				Data: map[string]any{"mode": spec},
			}
		}
	}
	key := spec
	if len(key) > 3 {
		key = key[len(key)-3:]
	}
	var perm uint32
	if _, err := fmt.Sscanf(key, "%o", &perm); err != nil {
		return 0, chmodModeDenied(spec)
	}
	mode := os.FileMode(perm & 0o777)
	if err := validateChmodResult(mode); err != nil {
		return 0, err
	}
	return mode, nil
}

func applySymbolicChmodMode(current os.FileMode, spec string) (os.FileMode, error) {
	who := "a"
	var op byte
	var perm string

	if len(spec) >= 2 && (spec[0] == '+' || spec[0] == '-') {
		op = spec[0]
		perm = spec[1:]
	} else {
		i := 0
		for i < len(spec) && strings.ContainsRune("ugoa", rune(spec[i])) {
			i++
		}
		if i == 0 || i >= len(spec) {
			return 0, chmodModeDenied(spec)
		}
		who = spec[:i]
		if spec[i] != '+' && spec[i] != '-' {
			return 0, chmodModeDenied(spec)
		}
		op = spec[i]
		perm = spec[i+1:]
	}
	if op != '+' && op != '-' {
		return 0, chmodModeDenied(spec)
	}
	if perm == "" {
		return 0, chmodModeDenied(spec)
	}
	for _, r := range perm {
		if !strings.ContainsRune("rwx", r) {
			return 0, chmodModeDenied(spec)
		}
	}

	cur := uint32(current.Perm())
	add := op == '+'
	if strings.ContainsRune(who, 'a') {
		who = "ugo"
	}
	for _, w := range who {
		var rBit, wBit, xBit uint32
		switch w {
		case 'u':
			rBit, wBit, xBit = 0o400, 0o200, 0o100
		case 'g':
			rBit, wBit, xBit = 0o040, 0o020, 0o010
		case 'o':
			rBit, wBit, xBit = 0o004, 0o002, 0o001
		default:
			return 0, chmodModeDenied(spec)
		}
		for _, r := range perm {
			var bit uint32
			switch r {
			case 'r':
				bit = rBit
			case 'w':
				bit = wBit
			case 'x':
				bit = xBit
			}
			if add {
				cur |= bit
			} else {
				cur &^= bit
			}
		}
	}
	mode := os.FileMode(cur)
	if err := validateChmodResult(mode); err != nil {
		return 0, err
	}
	return mode, nil
}

func validateChmodResult(mode os.FileMode) error {
	perm := uint32(mode & 0o777)
	if perm == 0o777 || perm == 0o666 {
		return chmodModeDenied(sourceview.FormatFileMode(perm))
	}
	if _, ok := allowedChmodPerm[perm]; !ok {
		return chmodModeDenied(sourceview.FormatFileMode(perm))
	}
	return nil
}

func chmodModeDenied(mode string) error {
	return &tools.ToolReject{
		Code: "CHMOD_MODE_DENIED",
		Data: map[string]any{"mode": mode, "chmod_allowed_modes": allowedChmodModes()},
	}
}

func allowedChmodModes() []string {
	modes := make([]string, 0, len(allowedChmodPerm))
	for mode := range allowedChmodPerm {
		modes = append(modes, sourceview.FormatFileMode(mode))
	}
	sort.Strings(modes)
	return modes
}
