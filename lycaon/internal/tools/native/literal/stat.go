package literal

import (
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// statEntry is one observed operand: its name as given, its metadata and,
// for a link, the target text.
type statEntry struct {
	name, target string
	st           fseffect.EntryStat
}

// statDirective renders one format directive; numeric reports whether a
// zero-padding flag applies.
type statDirective struct {
	render  func(statEntry) (string, bool)
	numeric bool
}

func text(render func(statEntry) string) statDirective {
	return statDirective{render: func(e statEntry) (string, bool) { return render(e), true }}
}

func number(value func(fseffect.EntryStat) int64) statDirective {
	return statDirective{render: func(e statEntry) (string, bool) { return strconv.FormatInt(value(e.st), 10), true }, numeric: true}
}

// posix renders a fact only a POSIX platform observes.
func posix(value func(fseffect.EntryStat) string, numeric bool) statDirective {
	return statDirective{render: func(e statEntry) (string, bool) {
		if !e.st.POSIX {
			return "", false
		}
		return value(e.st), true
	}, numeric: numeric}
}

func decimal(n uint64) string { return strconv.FormatUint(n, 10) }

// statTime is GNU's human timestamp, always in UTC.
func statTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000000 -0700") }

var (
	statName     = text(func(e statEntry) string { return e.name })
	statSize     = number(func(st fseffect.EntryStat) int64 { return st.Size })
	statMTime    = number(func(st fseffect.EntryStat) int64 { return st.ModTime.Unix() })
	statOctal    = statDirective{render: func(e statEntry) (string, bool) { return octalMode(e.st.Mode), true }, numeric: true}
	statMode     = text(func(e statEntry) string { return modeString(e.st.Mode) })
	statUID      = posix(func(st fseffect.EntryStat) string { return decimal(uint64(st.UID)) }, true)
	statGID      = posix(func(st fseffect.EntryStat) string { return decimal(uint64(st.GID)) }, true)
	statInode    = posix(func(st fseffect.EntryStat) string { return decimal(st.Inode) }, true)
	statLinks    = posix(func(st fseffect.EntryStat) string { return decimal(st.Links) }, true)
	statBlocks   = posix(func(st fseffect.EntryStat) string { return strconv.FormatInt(st.Blocks, 10) }, true)
	statIOBlock  = posix(func(st fseffect.EntryStat) string { return strconv.FormatInt(st.BlockSize, 10) }, true)
	statUser     = posix(func(st fseffect.EntryStat) string { return accountName(user.LookupId, st.UID) }, false)
	statGroup    = posix(func(st fseffect.EntryStat) string { return groupName(st.GID) }, false)
	statAccess   = posix(func(st fseffect.EntryStat) string { return statTime(st.AccessTime) }, false)
	statChange   = posix(func(st fseffect.EntryStat) string { return statTime(st.ChangeTime) }, false)
	statModified = text(func(e statEntry) string { return statTime(e.st.ModTime) })
)

func accountName(lookup func(string) (*user.User, error), id uint32) string {
	if found, err := lookup(strconv.FormatUint(uint64(id), 10)); err == nil {
		return found.Username
	}
	return "UNKNOWN"
}

func groupName(id uint32) string {
	if found, err := user.LookupGroupId(strconv.FormatUint(uint64(id), 10)); err == nil {
		return found.Name
	}
	return "UNKNOWN"
}

// quotedName is GNU's %N: the name, shell-quoted when it needs quoting, with
// a link's target.
func quotedName(e statEntry) string {
	name := shellQuote(e.name)
	if typeLetter(e.st.Mode) == 'l' {
		name += " -> " + shellQuote(e.target)
	}
	return name
}

func shellQuote(s string) string {
	plain := s != ""
	for i := 0; i < len(s) && plain; i++ {
		c := s[i]
		plain = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._/@%+=:,-", c) >= 0
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// gnuDirectives are GNU stat -c; bsdDirectives are BSD stat -f. The two
// dialects describe the same observation, never the host's own stat.
var gnuDirectives = map[string]statDirective{
	"n": statName, "N": text(quotedName), "s": statSize, "a": statOctal, "A": statMode, "Y": statMTime,
	"F": text(func(e statEntry) string { return gnuType(e.st) }),
	"u": statUID, "g": statGID, "U": statUser, "G": statGroup, "i": statInode, "h": statLinks,
	"b": statBlocks, "o": statIOBlock,
	"D":  posix(func(st fseffect.EntryStat) string { return strconv.FormatUint(st.Device, 16) }, false),
	"d":  posix(func(st fseffect.EntryStat) string { return decimal(st.Device) }, true),
	"Hd": posix(func(st fseffect.EntryStat) string { return decimal(uint64(st.DeviceMajor)) }, true),
	"Ld": posix(func(st fseffect.EntryStat) string { return decimal(uint64(st.DeviceMinor)) }, true),
	"x":  statAccess, "y": statModified, "z": statChange,
	"X": posix(func(st fseffect.EntryStat) string { return strconv.FormatInt(st.AccessTime.Unix(), 10) }, true),
	"Z": posix(func(st fseffect.EntryStat) string { return strconv.FormatInt(st.ChangeTime.Unix(), 10) }, true),
	"w": text(func(e statEntry) string {
		if e.st.BirthTime.IsZero() {
			return "-"
		}
		return statTime(e.st.BirthTime)
	}),
	"W": statDirective{render: func(e statEntry) (string, bool) {
		if e.st.BirthTime.IsZero() {
			return "0", true
		}
		return strconv.FormatInt(e.st.BirthTime.Unix(), 10), true
	}, numeric: true},
}

var bsdDirectives = map[string]statDirective{
	"N": statName, "z": statSize, "Lp": statOctal, "Sp": statMode, "m": statMTime,
	"HT": text(func(e statEntry) string { return bsdType(e.st) }),
	"u":  statUID, "g": statGID, "i": statInode,
}

// statPart is literal text followed by one directive with its printf flags.
type statPart struct {
	literal, directive string
	left, zero         bool
	width, precision   int
}

// parseStatFormat reads literal text, %% and %[-0][WIDTH][.PRECISION]NAME.
func parseStatFormat(format string, directives map[string]statDirective) ([]statPart, string, bool) {
	var out []statPart
	var literal strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			literal.WriteByte(format[i])
			continue
		}
		if i+1 < len(format) && format[i+1] == '%' {
			literal.WriteByte('%')
			i++
			continue
		}
		start := i
		part := statPart{precision: -1}
		j := i + 1
		for ; j < len(format) && (format[j] == '-' || format[j] == '0'); j++ {
			part.left = part.left || format[j] == '-'
			part.zero = part.zero || format[j] == '0'
		}
		for ; j < len(format) && format[j] >= '0' && format[j] <= '9'; j++ {
			part.width = part.width*10 + int(format[j]-'0')
		}
		if j < len(format) && format[j] == '.' {
			part.precision = 0
			for j++; j < len(format) && format[j] >= '0' && format[j] <= '9'; j++ {
				part.precision = part.precision*10 + int(format[j]-'0')
			}
		}
		for _, size := range []int{2, 1} {
			if j+size <= len(format) {
				if _, ok := directives[format[j:j+size]]; ok {
					part.directive = format[j : j+size]
					break
				}
			}
		}
		if part.directive == "" || part.width > 1<<10 || part.precision > 1<<10 {
			return nil, format[start:min(j+1, len(format))], false
		}
		part.literal = literal.String()
		literal.Reset()
		out = append(out, part)
		i = j + len(part.directive) - 1
	}
	return append(out, statPart{literal: literal.String()}), "", true
}

// apply renders one directive with its precision and padding.
func (p statPart) apply(value string, numeric bool) string {
	if p.precision >= 0 && !numeric && len(value) > p.precision {
		value = value[:p.precision]
	}
	if pad := p.width - len(value); pad > 0 {
		switch {
		case p.left:
			value += strings.Repeat(" ", pad)
		case p.zero && numeric:
			value = strings.Repeat("0", pad) + value
		default:
			value = strings.Repeat(" ", pad) + value
		}
	}
	return value
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
	format, directives := gnu, gnuDirectives
	if !hasGNU && !hasBSD {
		// The catalog's default is GNU's multi-line description.
		format = inv.spec.DefaultFormat
	}
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
		entry := statEntry{name: operand, st: st}
		if typeLetter(st.Mode) == 'l' {
			bound, err := inv.paths.Bind(inv.ctx, operand, false)
			if err == nil {
				entry.target, err = bound.Readlink()
			}
			if err != nil {
				return projectpaths.PathOperationError(operand, err)
			}
		}
		var line strings.Builder
		for _, part := range parsed {
			line.WriteString(part.literal)
			if part.directive == "" {
				continue
			}
			directive := directives[part.directive]
			value, available := directive.render(entry)
			if !available {
				return platformUnavailable("stat", "POSIX owner and inode metadata for %"+part.directive)
			}
			line.WriteString(part.apply(value, directive.numeric))
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
	err = projectpaths.PathOperationError(operand, err)
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
