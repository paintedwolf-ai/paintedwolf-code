package commandprogram

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// FieldPart is a contiguous span of an expanded Field. Literal spans leave
// Substitution nil; process-substitution spans carry the Substitution handle
// that produced Value.
type FieldPart struct {
	Value        string
	Substitution *ProcessSubstitution
}

// Field is one expanded argv word with the span of the source word it came from.
// Substitution is set when the field (or sliced option value) is a single
// process substitution; parts holds the span decomposition when one or more
// process substitutions are embedded in a larger word (such as --file=<(...)).
type Field struct {
	Value        string
	Span         toolcontract.SourceSpan
	Substitution *ProcessSubstitution
	parts        *[]FieldPart
}

// NewFieldWithParts constructs a Field from contiguous literal and
// process-substitution spans.
func NewFieldWithParts(value string, span toolcontract.SourceSpan, parts []FieldPart) Field {
	var coalesced []FieldPart
	hasSubst := false
	for _, part := range parts {
		if part.Value == "" && part.Substitution == nil {
			continue
		}
		if part.Substitution != nil {
			hasSubst = true
			coalesced = append(coalesced, part)
			continue
		}
		if len(coalesced) > 0 && coalesced[len(coalesced)-1].Substitution == nil {
			coalesced[len(coalesced)-1].Value += part.Value
			continue
		}
		coalesced = append(coalesced, FieldPart{Value: part.Value})
	}
	if !hasSubst {
		return Field{Value: value, Span: span}
	}
	if len(coalesced) == 1 && coalesced[0].Substitution != nil && coalesced[0].Value == value {
		return Field{Value: value, Span: span, Substitution: coalesced[0].Substitution}
	}
	return Field{Value: value, Span: span, parts: &coalesced}
}

// ProcessSubstitution returns the substitution stream when this Field is a
// single process-substitution operand or option value.
func (f Field) ProcessSubstitution() *ProcessSubstitution {
	if f.Substitution != nil {
		return f.Substitution
	}
	if f.parts != nil && len(*f.parts) == 1 && (*f.parts)[0].Substitution != nil {
		return (*f.parts)[0].Substitution
	}
	return nil
}

// Parts returns the contiguous literal and process-substitution spans of f,
// or nil when f contains no process substitutions.
func (f Field) Parts() []FieldPart {
	if f.parts != nil {
		return *f.parts
	}
	if f.Substitution != nil {
		return []FieldPart{{Value: f.Value, Substitution: f.Substitution}}
	}
	return nil
}

// SliceFrom returns the suffix of f starting at byte offset, preserving any
// process-substitution attachments in the suffix.
func (f Field) SliceFrom(offset int) Field {
	if offset <= 0 {
		return f
	}
	if offset >= len(f.Value) {
		return Field{Value: "", Span: f.Span}
	}
	suffix := f.Value[offset:]
	parts := f.Parts()
	if len(parts) == 0 {
		return Field{Value: suffix, Span: f.Span}
	}
	remaining := offset
	var sliced []FieldPart
	for _, part := range parts {
		if remaining >= len(part.Value) {
			remaining -= len(part.Value)
			continue
		}
		if remaining > 0 {
			sliced = append(sliced, FieldPart{Value: part.Value[remaining:]})
			remaining = 0
			continue
		}
		sliced = append(sliced, part)
	}
	return NewFieldWithParts(suffix, f.Span, sliced)
}

// Fields is an ordered argv. Values never reparse or rejoin the words.
type Fields []Field

func (f Fields) Values() []string {
	out := make([]string, len(f))
	for i, field := range f {
		out[i] = field.Value
	}
	return out
}

// ArgvBinder binds argv to the selected command's catalog forms.
type ArgvBinder interface {
	Bind(toolcontract.CommandSpecification, Fields) (BoundArgv, error)
}

// BoundOption is one occurrence of a catalog form. Key is the form's first
// spelling; Spelling is what the word selected (or "-N" for a shorthand).
type BoundOption struct {
	Key        string
	Spelling   string
	Scope      string
	ValueRole  string
	Values     Fields
	Terminator string
	Span       toolcontract.SourceSpan
}

// BoundItem is an option or operand in argv order; find-style utilities need
// the interleaving, simple utilities use Options and Operands.
type BoundItem struct {
	Option  *BoundOption
	Operand *Field
}

// BoundArgv is the typed option record of one utility form invocation.
// EndOfOptions records an explicit "--" so forms that require it can check.
type BoundArgv struct {
	Command      string
	Items        []BoundItem
	EndOfOptions bool
	Help         bool
}

func (b BoundArgv) Operands() Fields {
	var out Fields
	for _, item := range b.Items {
		if item.Operand != nil {
			out = append(out, *item.Operand)
		}
	}
	return out
}

// Options returns every non-global occurrence of the form keyed by its first spelling.
func (b BoundArgv) Options(key string) []BoundOption {
	return b.scoped(key, false)
}

// Global returns occurrences of a global-scope form (for example git -C).
func (b BoundArgv) Global(key string) []BoundOption {
	return b.scoped(key, true)
}

// Last returns the final occurrence, which wins for repeated scalar options.
func (b BoundArgv) Last(key string) (BoundOption, bool) {
	options := b.Options(key)
	if len(options) == 0 {
		return BoundOption{}, false
	}
	return options[len(options)-1], true
}

func (b BoundArgv) scoped(key string, global bool) []BoundOption {
	var out []BoundOption
	for _, item := range b.Items {
		if item.Option != nil && item.Option.Key == key && (item.Option.Scope == "global") == global {
			out = append(out, *item.Option)
		}
	}
	return out
}

// ArgvReview carries the selected form's options and operands for approval.
type ArgvReview struct {
	OperationID string           `json:"operation_id"`
	Command     string           `json:"command"`
	Options     []ReviewedOption `json:"options"`
	Operands    []string         `json:"operands"`
	PathTargets []PathTarget     `json:"path_targets,omitempty"`
}

// PathTarget is a bound native file operand and its exact access direction.
type PathTarget struct {
	Path  string `json:"path"`
	Write bool   `json:"write"`
}

type ReviewedOption struct {
	Option    string   `json:"option"`
	Scope     string   `json:"scope,omitempty"`
	ValueRole string   `json:"value_role"`
	Values    []string `json:"values"`
}

func (b BoundArgv) Review(operationID string) ArgvReview {
	review := ArgvReview{OperationID: operationID, Command: b.Command, Options: []ReviewedOption{}, Operands: b.Operands().Values()}
	for _, item := range b.Items {
		if option := item.Option; option != nil {
			review.Options = append(review.Options, ReviewedOption{Option: option.Key, Scope: option.Scope, ValueRole: option.ValueRole, Values: option.Values.Values()})
		}
	}
	return review
}

// Arguments is the review record as the native review/approval input value.
func (r ArgvReview) Arguments() map[string]any {
	options := make([]any, 0, len(r.Options))
	for _, option := range r.Options {
		values := make([]any, len(option.Values))
		for i, value := range option.Values {
			values[i] = value
		}
		entry := map[string]any{"option": option.Option, "value_role": option.ValueRole, "values": values}
		if option.Scope != "" {
			entry["scope"] = option.Scope
		}
		options = append(options, entry)
	}
	operands := make([]any, len(r.Operands))
	for i, operand := range r.Operands {
		operands[i] = operand
	}
	result := map[string]any{"operation_id": r.OperationID, "command": r.Command, "options": options, "operands": operands}
	if r.PathTargets != nil {
		// Grouped by direction, each target is one captured value, so a command
		// at the operand limit stays within the input capture's node bound.
		reads, writes := []any{}, []any{}
		for _, target := range r.PathTargets {
			if target.Write {
				writes = append(writes, target.Path)
			} else {
				reads = append(reads, target.Path)
			}
		}
		result["path_targets"] = map[string]any{"read": reads, "write": writes}
	}
	return result
}

// ArgvError carries an existing public rejection code and the exact word span.
type ArgvError struct {
	Code     string
	Command  string
	Word     string
	Expected string
	Span     toolcontract.SourceSpan
}

func (e *ArgvError) Error() string {
	return fmt.Sprintf("%s: %s %q: %s", e.Code, e.Command, e.Word, e.Expected)
}

const (
	argvOptionUnsupported = "COMMAND_OPTION_UNSUPPORTED"
	argvOperandInvalid    = "COMMAND_OPERAND_INVALID"
)

// CatalogBinder implements the language option grammar over catalog forms:
// exact spellings, --long[=value], clustered short flags with an attached or
// following value, declared -N shorthand, "--" and GNU-style permutation
// unless the command declares that its first operand ends option parsing.
// Commands with find-style primaries bind whole words only.
type CatalogBinder struct{}

var _ ArgvBinder = CatalogBinder{}

func (CatalogBinder) Bind(spec toolcontract.CommandSpecification, argv Fields) (BoundArgv, error) {
	names := strings.Fields(spec.Name)
	bound := BoundArgv{Command: spec.Name}
	if len(names) == 0 || len(argv) == 0 || !commandWord(spec, names[0], argv[0].Value) {
		return bound, fmt.Errorf("argv does not name command %q", spec.Name)
	}
	b := argvBinding{spec: spec, argv: argv, index: 1, bound: &bound}
	scope := ""
	if len(names) > 1 {
		if err := b.global(names[1:]); err != nil {
			return bound, err
		}
		scope = "subcommand"
	}
	return bound, b.options(scope)
}

func commandWord(spec toolcontract.CommandSpecification, name, word string) bool {
	if word == name {
		return true
	}
	for _, alias := range spec.Aliases {
		if word == alias {
			return true
		}
	}
	return false
}

type argvBinding struct {
	spec  toolcontract.CommandSpecification
	argv  Fields
	index int
	bound *BoundArgv
}

// global binds global-scope forms up to and including the subcommand words.
func (b *argvBinding) global(subcommand []string) error {
	for _, name := range subcommand {
		for {
			if b.index >= len(b.argv) {
				return b.fail(argvOperandInvalid, Field{Span: b.argv[len(b.argv)-1].Span}, "subcommand "+name)
			}
			word := b.argv[b.index]
			if word.Value == name {
				b.index++
				break
			}
			if !optionWord(word.Value) {
				return b.fail(argvOperandInvalid, word, "subcommand "+name)
			}
			if err := b.option(word, "global"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *argvBinding) options(scope string) error {
	switch b.spec.Name {
	case "echo":
		return b.leadingOptions(func(word string) bool {
			return len(word) > 1 && word[0] == '-' && strings.TrimLeft(word[1:], "neE") == ""
		})
	case "printf":
		return b.leadingOptions(func(string) bool { return false })
	case "jobs":
		return b.jobsOptions()
	case "ps":
		return b.psOptions()
	}
	operandsOnly := false
	for b.index < len(b.argv) {
		word := b.argv[b.index]
		switch {
		case operandsOnly || !optionWord(word.Value):
			b.index++
			operand := word
			b.bound.Items = append(b.bound.Items, BoundItem{Operand: &operand})
			operandsOnly = operandsOnly || b.spec.OperandsEndOptions
		case word.Value == "--":
			b.index++
			b.bound.EndOfOptions = true
			operandsOnly = true
		default:
			if err := b.option(word, scope); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *argvBinding) leadingOptions(valid func(string) bool) error {
	operandsOnly := false
	for b.index < len(b.argv) {
		word := b.argv[b.index]
		b.index++
		if !operandsOnly && word.Value == "--" {
			b.bound.EndOfOptions = true
			operandsOnly = true
			continue
		}
		if !operandsOnly && valid(word.Value) {
			for i := 1; i < len(word.Value); i++ {
				flag := "-" + word.Value[i:i+1]
				form, _ := b.exact(flag, "")
				b.record(form, flag, word, nil, "")
			}
			continue
		}
		operandsOnly = true
		operand := word
		b.bound.Items = append(b.bound.Items, BoundItem{Operand: &operand})
	}
	return nil
}

func (b *argvBinding) jobsOptions() error {
	operandsOnly := false
	for b.index < len(b.argv) {
		word := b.argv[b.index]
		b.index++
		switch {
		case !operandsOnly && word.Value == "--":
			b.bound.EndOfOptions = true
			operandsOnly = true
		case !operandsOnly && optionWord(word.Value):
			if strings.TrimLeft(word.Value[1:], "lp") != "" {
				return b.fail(argvOptionUnsupported, word, "catalogued option")
			}
			for i := 1; i < len(word.Value); i++ {
				flag := "-" + word.Value[i:i+1]
				b.record(toolcontract.OptionForm{Spellings: []string{flag}}, flag, word, nil, "")
			}
		default:
			operand := word
			b.bound.Items = append(b.bound.Items, BoundItem{Operand: &operand})
		}
	}
	return nil
}

func (b *argvBinding) psOptions() error {
	operandsOnly := false
	for b.index < len(b.argv) {
		word := b.argv[b.index]
		b.index++
		switch {
		case !operandsOnly && word.Value == "--":
			b.bound.EndOfOptions = true
			operandsOnly = true
		case !operandsOnly && optionWord(word.Value):
			if strings.HasPrefix(word.Value, "--") {
				return b.fail(argvOptionUnsupported, word, "catalogued option")
			}
			suffix := word.Value[1:]
			for i := 0; i < len(suffix); i++ {
				ch := suffix[i]
				flag := "-" + suffix[i:i+1]
				switch ch {
				case 'e', 'A', 'f', 'a', 'u', 'x', 'l':
					if ch == 'A' {
						flag = "-e"
					}
					b.record(toolcontract.OptionForm{Spellings: []string{flag}}, flag, word, nil, "")
				case 'o', 'p':
					rest := suffix[i+1:]
					var val Field
					if rest != "" {
						val = word.SliceFrom(i + 2)
					} else {
						if b.index >= len(b.argv) {
							return b.fail(argvOperandInvalid, word, "value for "+flag)
						}
						val = b.argv[b.index]
						b.index++
					}
					b.record(toolcontract.OptionForm{Spellings: []string{flag}, Arity: 1}, flag, word, Fields{val}, "")
					i = len(suffix)
				default:
					return b.fail(argvOptionUnsupported, word, "catalogued option")
				}
			}
		default:
			operand := word
			b.bound.Items = append(b.bound.Items, BoundItem{Operand: &operand})
		}
	}
	return nil
}

// optionWord excludes "-" (standard input) and words that do not start with a dash.
func optionWord(word string) bool { return len(word) > 1 && word[0] == '-' }

func (b *argvBinding) option(word Field, scope string) error {
	b.index++
	if form, ok := b.exact(word.Value, scope); ok {
		if word.Value == "--help" && b.spec.Name != "external" {
			b.bound.Help = true
		}
		return b.consume(form, word.Value, word, Field{}, false)
	}
	if word.Value == "--help" && b.spec.Name != "external" {
		b.bound.Help = true
		b.record(toolcontract.OptionForm{Spellings: []string{"--help"}, Scope: scope, ValueRole: "boolean"}, "--help", word, nil, "")
		return nil
	}
	if strings.HasPrefix(word.Value, "--") {
		name, _, attached := strings.Cut(word.Value, "=")
		form, ok := b.exact(name, scope)
		if !ok || !attached {
			return b.fail(argvOptionUnsupported, word, "catalogued option")
		}
		return b.consume(form, name, word, word.SliceFrom(len(name)+1), true)
	}
	suffix := word.Value[1:]
	if form, ok := b.shorthand(scope, "numeric"); ok && decimal(suffix) {
		return b.shorthandValue(form, word, word.SliceFrom(1))
	}
	if b.wholeWordPrimaries(scope) {
		return b.fail(argvOptionUnsupported, word, "catalogued primary")
	}
	if _, ok := b.exact("-"+suffix[:1], scope); !ok {
		if form, ok := b.shorthand(scope, "word"); ok {
			return b.shorthandValue(form, word, word.SliceFrom(1))
		}
		return b.fail(argvOptionUnsupported, word, "catalogued option")
	}
	for i := 0; i < len(suffix); i++ {
		spelling := "-" + suffix[i:i+1]
		form, ok := b.exact(spelling, scope)
		if !ok {
			return b.fail(argvOptionUnsupported, word, "catalogued short option "+spelling)
		}
		if form.Arity == 0 {
			b.record(form, spelling, word, nil, "")
			continue
		}
		rest := suffix[i+1:]
		return b.consume(form, spelling, word, word.SliceFrom(i+2), rest != "")
	}
	return nil
}

func (b *argvBinding) exact(spelling, scope string) (toolcontract.OptionForm, bool) {
	for _, form := range b.spec.Forms {
		if !formInScope(form.Scope, scope) {
			continue
		}
		for _, candidate := range form.Spellings {
			if candidate == spelling {
				return form, true
			}
		}
	}
	return toolcontract.OptionForm{}, false
}

// wholeWordPrimaries reports find-style single-dash multi-letter spellings;
// such commands neither cluster short options nor attach values.
func (b *argvBinding) wholeWordPrimaries(scope string) bool {
	for _, form := range b.spec.Forms {
		for _, spelling := range form.Spellings {
			if formInScope(form.Scope, scope) && len(spelling) > 2 && spelling[0] == '-' && spelling[1] != '-' {
				return true
			}
		}
	}
	return false
}

func formInScope(formScope, scope string) bool {
	if scope == "global" {
		return formScope == "global"
	}
	return formScope != "global"
}

func (b *argvBinding) shorthand(scope, kind string) (toolcontract.OptionForm, bool) {
	for _, form := range b.spec.Forms {
		if form.Shorthand == kind && formInScope(form.Scope, scope) {
			return form, true
		}
	}
	return toolcontract.OptionForm{}, false
}

func (b *argvBinding) shorthandValue(form toolcontract.OptionForm, word Field, value Field) error {
	b.record(form, word.Value, word, Fields{value}, "")
	return nil
}

// consume takes the form's values. attached reports an inline value: the
// remaining short-cluster suffix or a long option's "=value".
func (b *argvBinding) consume(form toolcontract.OptionForm, spelling string, word Field, value Field, attached bool) error {
	switch form.Arity {
	case 0:
		if attached {
			return b.fail(argvOperandInvalid, word, "no value for "+spelling)
		}
		b.record(form, spelling, word, nil, "")
	case -1:
		return b.optional(form, spelling, word, value, attached)
	case -2:
		if attached {
			return b.fail(argvOperandInvalid, word, "separate words for "+spelling)
		}
		return b.delimited(form, spelling, word)
	default:
		values := Fields{}
		if attached {
			if form.Arity != 1 || (!strings.HasPrefix(spelling, "--") && len(spelling) != 2) {
				return b.fail(argvOperandInvalid, word, "separate value words for "+spelling)
			}
			values = append(values, value)
		}
		for len(values) < form.Arity {
			if b.index >= len(b.argv) {
				return b.fail(argvOperandInvalid, word, form.ValueRole+" value for "+spelling)
			}
			values = append(values, b.argv[b.index])
			b.index++
		}
		b.record(form, spelling, word, values, "")
	}
	return nil
}

// optional handles forms that never take a following nonempty word: an
// attached value (--porcelain=v1), or, for optional-attached-or-empty, also
// a following empty word (GNU sed -i.bak, or BSD sed -i followed by an empty
// argument).
func (b *argvBinding) optional(form toolcontract.OptionForm, spelling string, word Field, value Field, attached bool) error {
	switch form.Consumption {
	case "optional-attached-value":
		var values Fields
		if attached {
			values = Fields{value}
		}
		b.record(form, spelling, word, values, "")
	case "optional-attached-or-empty":
		values := Fields{}
		if attached {
			values = append(values, value)
		} else if b.index < len(b.argv) && b.argv[b.index].Value == "" {
			values = append(values, b.argv[b.index])
			b.index++
		}
		b.record(form, spelling, word, values, "")
	default:
		return b.fail(argvOptionUnsupported, word, "catalogued consumption for "+spelling)
	}
	return nil
}

// delimited consumes a value list: words through ";" or a "+" that directly
// follows "{}" (find -exec), or words up to the next option word or the end
// (zip -x PATTERN ...).
func (b *argvBinding) delimited(form toolcontract.OptionForm, spelling string, word Field) error {
	switch form.Consumption {
	case "through-semicolon-or-plus":
	case "through-next-option":
		values := Fields{}
		for b.index < len(b.argv) && !optionWord(b.argv[b.index].Value) {
			values = append(values, b.argv[b.index])
			b.index++
		}
		if len(values) == 0 {
			return b.fail(argvOperandInvalid, word, form.ValueRole+" values for "+spelling)
		}
		b.record(form, spelling, word, values, "")
		return nil
	default:
		return b.fail(argvOptionUnsupported, word, "catalogued consumption for "+spelling)
	}
	values := Fields{}
	for b.index < len(b.argv) {
		next := b.argv[b.index]
		b.index++
		if next.Value == ";" || (next.Value == "+" && len(values) > 0 && values[len(values)-1].Value == "{}") {
			if len(values) == 0 {
				return b.fail(argvOperandInvalid, word, form.ValueRole+" before terminator for "+spelling)
			}
			b.record(form, spelling, word, values, next.Value)
			return nil
		}
		values = append(values, next)
	}
	return b.fail(argvOperandInvalid, word, "; or {} + terminator for "+spelling)
}

func (b *argvBinding) record(form toolcontract.OptionForm, spelling string, word Field, values Fields, terminator string) {
	b.bound.Items = append(b.bound.Items, BoundItem{Option: &BoundOption{Key: form.Spellings[0], Spelling: spelling, Scope: form.Scope, ValueRole: form.ValueRole, Values: values, Terminator: terminator, Span: word.Span}})
}

func (b *argvBinding) fail(code string, word Field, expected string) error {
	return &ArgvError{Code: code, Command: b.spec.Name, Word: word.Value, Expected: expected, Span: word.Span}
}

func decimal(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// WriteCatalogHelp writes a catalog command's help to out and returns a completed read-only result.
func WriteCatalogHelp(spec toolcontract.CommandSpecification, out io.Writer) (toolcontract.ProducerResult, error) {
	if out != nil {
		if _, err := io.WriteString(out, spec.Help()+"\n"); err != nil {
			return ownerResult(err), err
		}
	}
	return ordinaryResult(0), nil
}

type helpDispatcher struct {
	spec toolcontract.CommandSpecification
}

func (h helpDispatcher) Invoke(_ context.Context, _ OperationRequest, streams Streams) (toolcontract.ProducerResult, error) {
	return WriteCatalogHelp(h.spec, streams.Output)
}

// HelpDispatcher returns an in-process dispatcher that writes spec's catalog help.
func HelpDispatcher(spec toolcontract.CommandSpecification) OperationDispatcher {
	return helpDispatcher{spec: spec}
}
