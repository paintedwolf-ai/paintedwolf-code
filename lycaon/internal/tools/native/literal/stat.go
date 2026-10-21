package literal

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// statDirective renders one format directive from observed metadata.
type statDirective func(name string, st fseffect.EntryStat) (string, bool)

func posix(value func(fseffect.EntryStat) uint64) statDirective {
	return func(_ string, st fseffect.EntryStat) (string, bool) {
		if !st.POSIX {
			return "", false
		}
		return strconv.FormatUint(value(st), 10), true
	}
}

var (
	statUID   = posix(func(st fseffect.EntryStat) uint64 { return uint64(st.UID) })
	statGID   = posix(func(st fseffect.EntryStat) uint64 { return uint64(st.GID) })
	statInode = posix(func(st fseffect.EntryStat) uint64 { return st.Inode })
	statName  = func(name string, _ fseffect.EntryStat) (string, bool) { return name, true }
	statSize  = func(_ string, st fseffect.EntryStat) (string, bool) { return strconv.FormatInt(st.Size, 10), true }
	statMTime = func(_ string, st fseffect.EntryStat) (string, bool) {
		return strconv.FormatInt(st.ModTime.Unix(), 10), true
	}
	statOctal = func(_ string, st fseffect.EntryStat) (string, bool) { return octalMode(st.Mode), true }
	statMode  = func(_ string, st fseffect.EntryStat) (string, bool) { return modeString(st.Mode), true }
)

// gnuDirectives are GNU stat -c; bsdDirectives are BSD stat -f. The two
// dialects describe the same observation, never the host's own stat.
var gnuDirectives = map[string]statDirective{
	"n": statName, "s": statSize, "a": statOctal, "A": statMode, "Y": statMTime,
	"F": func(_ string, st fseffect.EntryStat) (string, bool) { return gnuType(st), true },
	"u": statUID, "g": statGID, "i": statInode,
}

var bsdDirectives = map[string]statDirective{
	"N": statName, "z": statSize, "Lp": statOctal, "Sp": statMode, "m": statMTime,
	"HT": func(_ string, st fseffect.EntryStat) (string, bool) { return bsdType(st), true },
	"u": statUID, "g": statGID, "i": statInode,
}

// statFormat is a parsed format: literal text and directive names.
type statFormat []struct{ literal, directive string }

func parseStatFormat(format string, directives map[string]statDirective) (statFormat, string, bool) {
	var out statFormat
	var text strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			text.WriteByte(format[i])
			continue
		}
		matched := ""
		for _, width := range []int{2, 1} {
			if i+1+width <= len(format) {
				if _, ok := directives[format[i+1:i+1+width]]; ok {
					matched = format[i+1 : i+1+width]
					break
				}
			}
		}
		switch {
		case i+1 < len(format) && format[i+1] == '%':
			text.WriteByte('%')
			i++
		case matched != "":
			out = append(out, struct{ literal, directive string }{text.String(), matched})
			text.Reset()
			i += len(matched)
		default:
			end := min(i+3, len(format))
			return nil, format[i:end], false
		}
	}
	return append(out, struct{ literal, directive string }{text.String(), ""}), "", true
}

func runStat(inv *invocation) error {
	if inv.flag("--file-system") {
		return platformUnavailable("stat", "file-system metadata")
	}
	gnu, hasGNU := inv.value("-c")
	bsd, hasBSD := inv.value("-f")
	if hasGNU && hasBSD {
		return inv.usage("-f", "one of -c GNU_FORMAT or -f BSD_FORMAT")
	}
	if !hasGNU && !hasBSD {
		return inv.usage("stat", "a -c GNU_FORMAT or -f BSD_FORMAT selection")
	}
	format, directives := gnu, gnuDirectives
	if hasBSD {
		format, directives = bsd, bsdDirectives
	}
	parsed, bad, ok := parseStatFormat(format, directives)
	if !ok {
		return inv.usage(bad, "catalogued stat directive")
	}
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.usage("stat", "at least one PATH")
	}
	follow := inv.flag("-L")
	for _, operand := range operands {
		st, ok, err := inv.entry(operand, follow)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		var line strings.Builder
		for _, part := range parsed {
			line.WriteString(part.literal)
			if part.directive == "" {
				continue
			}
			value, available := directives[part.directive](operand, st)
			if !available {
				return platformUnavailable("stat", "POSIX owner and inode metadata for %"+part.directive)
			}
			line.WriteString(value)
		}
		line.WriteByte('\n')
		if err := inv.out.text(line.String()); err != nil {
			return err
		}
	}
	return nil
}

// entry observes one operand's metadata through its path binding. A link is
// reported itself unless follow; ordinary failures are reported and skipped.
func (inv *invocation) entry(operand string, follow bool) (fseffect.EntryStat, bool, error) {
	bound, err := inv.paths.Bind(inv.ctx, operand, false)
	if err == nil {
		var st fseffect.EntryStat
		if st, err = bound.Stat(follow); err == nil {
			return st, true, nil
		}
	}
	if reason, ok := ordinary(err); ok {
		inv.fail(inv.entryFailureStatus(), operand, reason)
		return fseffect.EntryStat{}, false, nil
	}
	return fseffect.EntryStat{}, false, err
}

// entryFailureStatus: GNU ls treats an inaccessible command-line operand as
// serious trouble (2); diff reserves 1 for differences.
func (inv *invocation) entryFailureStatus() int {
	if inv.name() == "ls" {
		return 2
	}
	return inv.failureStatus()
}
