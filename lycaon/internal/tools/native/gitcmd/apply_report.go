package gitcmd

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/native/patchapply"
)

// writeReports prints --stat, --numstat and --summary the way git apply does.
func writeReports(w io.Writer, argv commandprogram.BoundArgv, sections []*applySection) {
	if has(argv, "--stat") {
		writeStat(w, sections)
	}
	if has(argv, "--numstat") {
		for _, s := range sections {
			added, deleted := lineCounts(s.file.File)
			if s.file.IsBinary {
				_, _ = fmt.Fprintf(w, "-\t-\t%s\n", quoteName(s.name))
				continue
			}
			_, _ = fmt.Fprintf(w, "%d\t%d\t%s\n", added, deleted, quoteName(s.name))
		}
	}
	if has(argv, "--summary") {
		for _, s := range sections {
			writeSummary(w, s)
		}
	}
}

func lineCounts(f *gitdiff.File) (int, int) {
	added, deleted := 0, 0
	for _, frag := range f.TextFragments {
		added += int(frag.LinesAdded)
		deleted += int(frag.LinesDeleted)
	}
	return added, deleted
}

// writeStat prints the diffstat: names padded to the longest (at most 50),
// bars scaled to fit 70 columns, then the files-changed summary line.
func writeStat(w io.Writer, sections []*applySection) {
	maxLen, maxChange, adds, dels := 0, 0, 0, 0
	for _, s := range sections {
		maxLen = max(maxLen, len(quoteName(s.name)))
		added, deleted := lineCounts(s.file.File)
		maxChange = max(maxChange, added+deleted)
	}
	for _, s := range sections {
		name := quoteName(s.name)
		width := min(maxLen, 50)
		if len(name) > width {
			cut := len(name) + 3 - width
			if slash := strings.IndexByte(name[cut:], '/'); slash >= 0 {
				cut += slash
			}
			name = "..." + name[cut:]
		}
		if s.file.IsBinary {
			_, _ = fmt.Fprintf(w, " %-*s |  Bin\n", width, name)
			continue
		}
		added, deleted := lineCounts(s.file.File)
		adds, dels = adds+added, dels+deleted
		bars := maxChange
		if width+maxChange > 70 {
			bars = 70 - width
		}
		plus, minus := added, deleted
		if maxChange > 0 {
			total := ((added+deleted)*bars + maxChange/2) / maxChange
			plus = (added*bars + maxChange/2) / maxChange
			minus = total - plus
		}
		_, _ = fmt.Fprintf(w, " %-*s |%5d %s%s\n", width, name, added+deleted, strings.Repeat("+", plus), strings.Repeat("-", minus))
	}
	files := len(sections)
	summary := fmt.Sprintf(" %d %s changed", files, pluralWord(files, "file", "files"))
	if adds > 0 || dels == 0 {
		summary += fmt.Sprintf(", %d %s", adds, pluralWord(adds, "insertion(+)", "insertions(+)"))
	}
	if dels > 0 || adds == 0 {
		summary += fmt.Sprintf(", %d %s", dels, pluralWord(dels, "deletion(-)", "deletions(-)"))
	}
	_, _ = fmt.Fprintln(w, summary)
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// writeSummary prints one section's creation, deletion, rename, copy,
// rewrite and mode-change lines.
func writeSummary(w io.Writer, s *applySection) {
	f := s.file
	switch {
	case f.IsNew:
		_, _ = fmt.Fprintf(w, " create mode %06o %s\n", uint32(f.NewMode), s.newName)
	case f.IsDelete:
		_, _ = fmt.Fprintf(w, " delete mode %06o %s\n", uint32(f.OldMode), s.oldName)
	case f.IsRename || f.IsCopy:
		kind := "rename"
		if f.IsCopy {
			kind = "copy"
		}
		oldName, newName := s.oldName, s.newName
		for {
			oldSlash, newSlash := strings.IndexByte(oldName, '/'), strings.IndexByte(newName, '/')
			if oldSlash < 0 || oldSlash != newSlash || oldName[:oldSlash] != newName[:newSlash] {
				break
			}
			oldName, newName = oldName[oldSlash+1:], newName[newSlash+1:]
		}
		if prefix := s.oldName[:len(s.oldName)-len(oldName)]; prefix != "" {
			_, _ = fmt.Fprintf(w, " %s %s{%s => %s} (%d%%)\n", kind, prefix, oldName, newName, f.Score)
		} else {
			_, _ = fmt.Fprintf(w, " %s %s => %s (%d%%)\n", kind, s.oldName, s.newName, f.Score)
		}
		writeModeChange(w, f, "")
	case f.Score > 0:
		_, _ = fmt.Fprintf(w, " rewrite %s (%d%%)\n", s.newName, f.Score)
		writeModeChange(w, f, "")
	default:
		writeModeChange(w, f, " "+s.newName)
	}
}

func writeModeChange(w io.Writer, f patchapply.File, name string) {
	if f.OldMode != 0 && f.NewMode != 0 && f.OldMode != f.NewMode {
		_, _ = fmt.Fprintf(w, " mode change %06o => %06o%s\n", uint32(f.OldMode), uint32(f.NewMode), name)
	}
}

// quoteName C-quotes a path holding quotes, backslashes, control or
// non-ASCII bytes, as git apply prints it.
func quoteName(name string) string {
	needs := false
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c == '"' || c == '\\' || c >= 0x7f {
			needs = true
			break
		}
	}
	if !needs {
		return name
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(name); i++ {
		switch c := name[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if c < 0x20 || c >= 0x7f {
				fmt.Fprintf(&b, `\%03o`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// wildmatch is Git's pattern match without WM_PATHNAME: * and ? match any
// byte including /, and [...] is a byte class.
func wildmatch(pattern, name string) bool {
	var expr strings.Builder
	expr.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			expr.WriteString(".*")
		case '?':
			expr.WriteString(".")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				expr.WriteString(`\[`)
				continue
			}
			class := pattern[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			expr.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case '\\':
			if i+1 < len(pattern) {
				i++
				expr.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			}
		default:
			expr.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	expr.WriteString("$")
	re, err := regexp.Compile(expr.String())
	return err == nil && re.MatchString(name)
}
