package search

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
)

type grepMode int

const (
	modeLines grepMode = iota
	modeCount
	modeFilesWith
	modeFilesWithout
	modeQuiet
	modeFileList
)

// grepRun is one bound grep or rg invocation.
type grepRun struct {
	inv     *invocation
	rg      bool
	matcher *textsearch.Matcher
	scan    textsearch.ScanOptions
	mode    grepMode
	only    bool
	numbers bool
	// offsets (-b), tabs (-T) and nullNames (-Z) shape record prefixes.
	offsets, tabs, nullNames bool
	numberWidth              int
	// names is the forced -H/-h choice; nil decides per file.
	names       *bool
	quietErrors bool
	replace     *string
	recursive   bool
	follow      bool
	selection   selection
	// binaryMode is binary (notice), text (-a) or without-match (-I).
	binaryMode      string
	skipDirectories bool
	walker          *walker
	// json is rg --json's printer; nil for the plain forms.
	json     *rgJSON
	index    grepContentIndex
	selected bool
	stopped  bool
	operands commandprogram.Fields
	files    *grepFiles
}

func runGrep(inv *invocation) error {
	r := &grepRun{inv: inv, scan: textsearch.ScanOptions{MaxSelected: -1}}
	dialect, err := r.grepDialect()
	if err != nil {
		return err
	}
	r.recursive = inv.flag("-r") || inv.flag("-R")
	r.follow = inv.flag("-R")
	if err := r.bsdModes(); err != nil {
		return err
	}
	r.quietErrors = inv.flag("-s")
	r.names = lastChoice(inv, "-H", "-h")
	r.selection = newSelection(inv, false)
	if r.selection.err != nil {
		return r.selection.err
	}
	return r.run(textsearch.Spec{Dialect: dialect, IgnoreCase: inv.flag("-i"), Word: inv.flag("-w"), Line: inv.flag("-x")})
}

func runRg(inv *invocation) error {
	if inv.flag("-h") {
		return inv.out.text(rgHelp(inv.spec))
	}
	r := &grepRun{inv: inv, rg: true, scan: textsearch.ScanOptions{MaxSelected: -1}, recursive: true}
	dialect := textsearch.RE2
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-F":
			dialect = textsearch.Fixed
		case "-E":
			dialect = textsearch.Extended
		case "-P":
			dialect = textsearch.Perl
		}
	}
	r.follow = inv.flag("-L")
	r.quietErrors = inv.flag("--no-messages")
	if err := r.color(); err != nil {
		return err
	}
	if inv.flag("--no-filename") {
		no := false
		r.names = &no
	} else if inv.flag("-H") {
		yes := true
		r.names = &yes
	}
	if replacement, ok := inv.value("-r"); ok {
		r.replace = &replacement
	}
	r.selection = newSelection(inv, true)
	if err := r.selection.compileRg(); err != nil {
		return err
	}
	if inv.flag("--files") {
		r.mode = modeFileList
		r.operands = inv.argv.Operands()
		return r.searchOperands()
	}
	spec := textsearch.Spec{Dialect: dialect, Word: inv.flag("-w")}
	r.rgCase(&spec)
	return r.run(spec)
}

// grepDialect rejects conflicting matcher selections.
func (r *grepRun) grepDialect() (textsearch.Dialect, error) {
	chosen := map[textsearch.Dialect]bool{}
	switch r.inv.word() {
	case "egrep":
		chosen[textsearch.Extended] = true
	case "fgrep":
		chosen[textsearch.Fixed] = true
	}
	for key, dialect := range map[string]textsearch.Dialect{"-E": textsearch.Extended, "-F": textsearch.Fixed, "-P": textsearch.Perl, "-G": textsearch.Basic} {
		if r.inv.flag(key) {
			chosen[dialect] = true
		}
	}
	if len(chosen) > 1 {
		return "", r.inv.usage(r.inv.word(), "one matcher; conflicting matchers specified")
	}
	for dialect := range chosen {
		return dialect, nil
	}
	return textsearch.Basic, nil
}

// rgCase applies rg's last-wins case selection among -i, -S and -s.
func (r *grepRun) rgCase(spec *textsearch.Spec) {
	mode := ""
	for _, item := range r.inv.argv.Items {
		if item.Option != nil && (item.Option.Key == "-i" || item.Option.Key == "-S" || item.Option.Key == "-s") {
			mode = item.Option.Key
		}
	}
	switch mode {
	case "-i":
		spec.IgnoreCase = true
	case "-S":
		spec.IgnoreCase = r.smartCaseLower()
	}
}

// smartCaseLower is rg -S: case-insensitive unless a pattern holds an
// uppercase literal (escape sequences are not literals).
func (r *grepRun) smartCaseLower() bool {
	patterns, _ := r.patternWords()
	for _, p := range patterns {
		for i := 0; i < len(p); {
			if p[i] == '\\' {
				i += 2
				continue
			}
			c, size := utf8.DecodeRuneInString(p[i:])
			if unicode.IsUpper(c) {
				return false
			}
			i += size
		}
	}
	return true
}

func lastChoice(inv *invocation, yesKey, noKey string) *bool {
	var choice *bool
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case yesKey:
			yes := true
			choice = &yes
		case noKey:
			no := false
			choice = &no
		}
	}
	return choice
}

// patternWords returns -e values in order or, without -e/-f, the first
// operand; it reports whether the first operand was consumed.
func (r *grepRun) patternWords() ([]string, bool) {
	var words []string
	explicit := false
	for _, item := range r.inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-e":
			words = append(words, item.Option.Values[0].Value)
			explicit = true
		case "-f":
			explicit = true
		}
	}
	operands := r.inv.argv.Operands().Values()
	if !explicit && len(operands) > 0 {
		return operands[:1], true
	}
	return words, false
}

// patterns gathers -e values and -f files in argument order. A pattern file
// is pinned like any other read; "-f -" reads standard input.
func (r *grepRun) patterns() ([]string, commandprogram.Fields, error) {
	var patterns []string
	explicit := false
	for _, item := range r.inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-e":
			patterns = append(patterns, item.Option.Values[0].Value)
			explicit = true
		case "-f":
			explicit = true
			content, err := r.patternFile(item.Option.Values[0])
			if err != nil {
				return nil, nil, err
			}
			if len(content) > 0 {
				patterns = append(patterns, strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")...)
			}
		}
	}
	operands := r.inv.argv.Operands()
	if explicit {
		return patterns, operands, nil
	}
	if len(operands) == 0 {
		return nil, nil, r.inv.usage(r.inv.word(), "a PATTERN operand or -e PATTERN")
	}
	return []string{operands[0].Value}, operands[1:], nil
}

func (r *grepRun) patternFile(field commandprogram.Field) ([]byte, error) {
	if field.Value == "-" {
		return r.inv.readAll(r.inv.input)
	}
	if reader, ok := field.ProcessSubstitution().OpenReader(); ok {
		defer func() { _ = reader.Close() }()
		return r.inv.readAll(reader)
	}
	bound, err := r.inv.paths.Bind(r.inv.ctx, field.Value, false)
	if err != nil {
		return nil, err
	}
	if bound.Null {
		return nil, nil
	}
	content, err := r.inv.capture(bound)
	if reason, ok := ordinary(err); ok {
		r.inv.fail(false, "%s: %s", field.Value, reason)
		return nil, errUsage
	}
	return content, err
}

func (r *grepRun) count(key string) (int, bool, error) {
	value, ok := r.inv.value(key)
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, false, r.inv.usage(value, "a nonnegative decimal count for "+key)
	}
	return n, true, nil
}

func (r *grepRun) run(spec textsearch.Spec) error {
	inv := r.inv
	patterns, operands, err := r.patterns()
	if err != nil {
		return err
	}
	spec.Patterns = patterns
	r.operands = operands
	inv.noMatchPublishes = true
	r.scan.Invert = inv.flag("-v")
	r.only = inv.flag("-o")
	r.numbers = inv.flag("-n")
	if !r.rg {
		r.offsets, r.tabs, r.nullNames, r.scan.NullData = inv.flag("-b"), inv.flag("-T"), inv.flag("-Z"), inv.flag("-z")
	}
	if err := r.color(); err != nil {
		return err
	}
	switch {
	case inv.flag("-q"):
		r.mode = modeQuiet
	case inv.flag("-l"):
		r.mode = modeFilesWith
	case !r.rg && inv.flag("-L") || r.rg && inv.flag("--files-without-match"):
		r.mode = modeFilesWithout
	case inv.flag("-c"):
		r.mode = modeCount
	}
	if r.scan.MaxSelected, _, err = r.maxCount(); err != nil {
		return err
	}
	if err := r.context(); err != nil {
		return err
	}
	limits := inv.owners.regex
	if memory := inv.owners.Revisions.Memory; memory != nil {
		limits.Reserve = memory.Reserve
	}
	if r.matcher, err = textsearch.Compile(inv.ctx, spec, limits); err != nil {
		return err
	}
	if r.rg && inv.flag("--json") && r.mode == modeLines {
		r.json = &rgJSON{started: time.Now()}
	}
	r.index = newGrepContentIndex(r.inv, r.matcher.Requirement(), !r.scan.Invert && r.json == nil && !(r.rg && r.mode == modeFilesWithout))
	defer r.index.completed(inv)
	if r.replace != nil {
		if err := checkReplacement(*r.replace, r.matcher.CaptureGroups()); err != nil {
			return inv.usage(*r.replace, err.Error())
		}
	}
	if err := r.searchOperands(); err != nil {
		return err
	}
	if r.json != nil {
		if err := inv.out.text(r.json.summary()); err != nil {
			return err
		}
	}
	switch {
	case inv.status == inv.failure && !(r.mode == modeQuiet && r.selected):
	case r.selected:
		inv.status = 0
	default:
		inv.status = 1
	}
	return nil
}

func (r *grepRun) maxCount() (int, bool, error) {
	n, ok, err := r.count("-m")
	if err != nil || !ok {
		return -1, false, err
	}
	return n, true, nil
}

// context applies -C as the default for whichever of -A and -B is unset.
func (r *grepRun) context() error {
	both, haveBoth, err := r.count("-C")
	if err != nil {
		return err
	}
	after, haveAfter, err := r.count("-A")
	if err != nil {
		return err
	}
	before, haveBefore, err := r.count("-B")
	if err != nil {
		return err
	}
	if !haveAfter && haveBoth {
		after = both
	}
	if !haveBefore && haveBoth {
		before = both
	}
	r.scan.After, r.scan.Before = after, before
	return nil
}

// color permits automatic color only; output has no terminal.
func (r *grepRun) color() error {
	option, ok := r.inv.argv.Last("--color")
	if !ok || len(option.Values) == 0 {
		return nil
	}
	switch option.Values[0].Value {
	case "never", "auto", "no", "none", "tty", "if-tty":
		return nil
	}
	return r.inv.usage(option.Values[0].Value, "--color=never or --color=auto")
}

// bsdModes resolves repeated binary, directory, device, and link options.
func (r *grepRun) bsdModes() error {
	r.binaryMode = "binary"
	for _, item := range r.inv.argv.Items {
		option := item.Option
		if option == nil {
			continue
		}
		value := ""
		if len(option.Values) > 0 {
			value = option.Values[0].Value
		}
		switch option.Key {
		case "-a":
			r.binaryMode = "text"
		case "-I":
			r.binaryMode = "without-match"
		case "--binary-files":
			switch value {
			case "binary", "text", "without-match":
				r.binaryMode = value
			default:
				return r.inv.usage(value, "--binary-files=binary, text or without-match")
			}
		case "-d":
			switch value {
			case "read":
				r.skipDirectories = false
			case "skip":
				r.skipDirectories = true
			case "recurse":
				r.recursive = true
			default:
				return r.inv.usage(value, "-d read, skip or recurse")
			}
		case "-D":
			if value != "read" && value != "skip" {
				return r.inv.usage(value, "-D read or skip")
			}
		case "-S":
			r.follow = true
		case "-p", "-O":
			r.follow = false
		}
	}
	return nil
}
