package pongoplain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/flosch/pongo2/v6"
)

// execStateKey carries the per-render executionState in pongo2.Context. It
// satisfies pongo2's identifier check while lexing as TokenNumber in templates,
// so template expressions and bindings cannot reference or shadow it.
const execStateKey = "0"

type executionAbort struct{ err error }

type executionState struct {
	ctx          context.Context
	steps        int
	forDepth     int
	includeDepth int
	includes     int
	onStep       func(step int)
}

func (s *executionState) checkContext() {
	if err := s.ctx.Err(); err != nil {
		panic(executionAbort{err: fmt.Errorf("%w: %w", ErrExecutionLimit, err)})
	}
}

func (s *executionState) step() {
	s.checkContext()
	s.steps++
	if s.steps > MaxExecutionSteps {
		panic(executionAbort{err: fmt.Errorf("%w: more than %d execution steps", ErrExecutionLimit, MaxExecutionSteps)})
	}
	if s.onStep != nil {
		s.onStep(s.steps)
		s.checkContext()
	}
}

func execStateFrom(ctx *pongo2.ExecutionContext) *executionState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Public[execStateKey].(*executionState)
	return state
}

func requireExecState(ctx *pongo2.ExecutionContext) (*executionState, *pongo2.Error) {
	if state := execStateFrom(ctx); state != nil {
		return state, nil
	}
	missing := fmt.Errorf("%w: missing execution state", ErrExecutionLimit)
	if ctx != nil {
		return nil, ctx.OrigError(missing, nil)
	}
	return nil, &pongo2.Error{Sender: "execution", OrigError: missing}
}

type boundedWriter struct {
	state *executionState
	buf   bytes.Buffer
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.state.checkContext()
	w.state.step()
	if len(p) > MaxOutputBytes-w.buf.Len() {
		panic(executionAbort{err: fmt.Errorf("%w: more than %d bytes", ErrOutputLimit, MaxOutputBytes)})
	}
	return w.buf.Write(p)
}

func (w *boundedWriter) WriteString(s string) (int, error) {
	w.state.checkContext()
	w.state.step()
	if len(s) > MaxOutputBytes-w.buf.Len() {
		panic(executionAbort{err: fmt.Errorf("%w: more than %d bytes", ErrOutputLimit, MaxOutputBytes)})
	}
	return w.buf.WriteString(s)
}

func wrapPongoError(prefix string, err error) error {
	var pErr *pongo2.Error
	if errors.As(err, &pErr) && pErr.OrigError != nil {
		return fmt.Errorf("%s: %s: %w", prefix, pErr.Error(), pErr.OrigError)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// Execute renders with a bounded data-only context and deterministic step budget.
func Execute(ctx context.Context, tpl *pongo2.Template, values map[string]any) (string, error) {
	return executeWithHook(ctx, tpl, values, nil)
}

func executeWithHook(
	ctx context.Context,
	tpl *pongo2.Template,
	values map[string]any,
	onStep func(step int),
) (out string, err error) {
	if tpl == nil {
		return "", fmt.Errorf("pongoplain: execute nil template")
	}
	if ctx == nil {
		return "", fmt.Errorf("pongoplain: execute context required")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("%w: %w", ErrExecutionLimit, ctxErr)
	}
	normalized, normErr := normalizeContext(values)
	if normErr != nil {
		return "", normErr
	}
	state := &executionState{ctx: ctx, onStep: onStep}
	normalized[execStateKey] = state

	defer func() {
		if recovered := recover(); recovered != nil {
			out = ""
			if abort, ok := recovered.(executionAbort); ok {
				err = abort.err
			} else {
				err = errors.New("pongoplain: internal execution panic")
			}
		}
	}()

	writer := &boundedWriter{state: state}
	if execErr := tpl.ExecuteWriterUnbuffered(pongo2.Context(normalized), writer); execErr != nil {
		return "", wrapPongoError("pongoplain: execute", execErr)
	}
	return writer.buf.String(), nil
}

// ExecuteDetached renders under deterministic resource and execution bounds.
func ExecuteDetached(tpl *pongo2.Template, values map[string]any) (string, error) {
	return Execute(context.Background(), tpl, values)
}

type contextBudget struct {
	nodes int
	bytes int
	seen  map[visit]bool
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func normalizeContext(values map[string]any) (map[string]any, error) {
	if values == nil {
		return map[string]any{}, nil
	}
	b := &contextBudget{seen: make(map[visit]bool)}
	v, err := b.normalize(reflect.ValueOf(values), 0)
	if err != nil {
		return nil, err
	}
	return v.Interface().(map[string]any), nil
}

func (b *contextBudget) normalize(v reflect.Value, depth int) (reflect.Value, error) {
	if depth > MaxContextDepth {
		return reflect.Value{}, fmt.Errorf("%w: nesting exceeds %d", ErrContextLimit, MaxContextDepth)
	}
	b.nodes++
	if b.nodes > MaxContextNodes {
		return reflect.Value{}, fmt.Errorf("%w: more than %d values", ErrContextLimit, MaxContextNodes)
	}
	if !v.IsValid() {
		return reflect.ValueOf(nil), nil
	}
	var visits []visit
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.ValueOf(nil), nil
		}
		if v.Kind() == reflect.Pointer {
			key := visit{typ: v.Type(), ptr: v.Pointer()}
			if b.seen[key] {
				return reflect.Value{}, fmt.Errorf("%w: cyclic pointer graph", ErrContextLimit)
			}
			b.seen[key] = true
			visits = append(visits, key)
		}
		v = v.Elem()
	}
	defer func() {
		for _, key := range visits {
			delete(b.seen, key)
		}
	}()

	switch v.Kind() {
	case reflect.Bool:
		return reflect.ValueOf(v.Bool()), nil
	case reflect.String:
		if err := b.addBytes(v.Len()); err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(v.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint32, reflect.Uint64, reflect.Uint16, reflect.Uintptr:
		return reflect.ValueOf(v.Uint()), nil
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(v.Float()), nil
	case reflect.Map:
		return b.normalizeMap(v, depth)
	case reflect.Slice, reflect.Array:
		return b.normalizeList(v, depth)
	case reflect.Struct:
		return b.normalizeStruct(v, depth)
	case reflect.Invalid:
		return reflect.ValueOf(nil), nil
	default:
		return reflect.Value{}, fmt.Errorf("%w: unsupported value type %s", ErrContextLimit, v.Type())
	}
}

func (b *contextBudget) normalizeMap(v reflect.Value, depth int) (reflect.Value, error) {
	if v.Type().Key().Kind() != reflect.String {
		return reflect.Value{}, fmt.Errorf("%w: map key type %s is not string", ErrContextLimit, v.Type().Key())
	}
	limit := MaxCollectionItems
	if depth == 0 {
		limit = MaxContextEntries
	}
	if v.Len() > limit {
		return reflect.Value{}, fmt.Errorf("%w: map has %d entries", ErrContextLimit, v.Len())
	}
	out := make(map[string]any, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		key := iter.Key().String()
		if depth == 0 && key == execStateKey {
			return reflect.Value{}, fmt.Errorf("%w: reserved context key %q", ErrContextLimit, key)
		}
		if err := b.addBytes(len(key)); err != nil {
			return reflect.Value{}, err
		}
		item, err := b.normalize(iter.Value(), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		out[key] = valueInterface(item)
	}
	return reflect.ValueOf(out), nil
}

func (b *contextBudget) addBytes(count int) error {
	b.bytes += count
	if b.bytes > MaxContextBytes {
		return fmt.Errorf("%w: strings exceed %d bytes", ErrContextLimit, MaxContextBytes)
	}
	return nil
}

func (b *contextBudget) normalizeList(v reflect.Value, depth int) (reflect.Value, error) {
	if v.Len() > MaxCollectionItems {
		return reflect.Value{}, fmt.Errorf("%w: collection has %d items", ErrContextLimit, v.Len())
	}
	out := make([]any, v.Len())
	for i := 0; i < v.Len(); i++ {
		item, err := b.normalize(v.Index(i), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		out[i] = valueInterface(item)
	}
	return reflect.ValueOf(out), nil
}

func (b *contextBudget) normalizeStruct(v reflect.Value, depth int) (reflect.Value, error) {
	out := make(map[string]any)
	typ := v.Type()
	exported := 0
	for i := 0; i < v.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		exported++
		if err := b.addBytes(len(field.Name)); err != nil {
			return reflect.Value{}, err
		}
		item, err := b.normalize(v.Field(i), depth+1)
		if err != nil {
			return reflect.Value{}, err
		}
		value := valueInterface(item)
		out[field.Name] = value
		for _, tagName := range []string{"json", "yaml"} {
			if alias := strings.Split(field.Tag.Get(tagName), ",")[0]; alias != "" && alias != "-" {
				if err := b.addBytes(len(alias)); err != nil {
					return reflect.Value{}, err
				}
				out[alias] = value
			}
		}
	}
	if exported == 0 && v.NumField() > 0 {
		return reflect.Value{}, fmt.Errorf("%w: struct %s exposes no data fields", ErrContextLimit, typ)
	}
	return reflect.ValueOf(out), nil
}

func valueInterface(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	return v.Interface()
}
