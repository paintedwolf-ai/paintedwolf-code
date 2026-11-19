package httpaction

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// JarOutput screens one invocation's delivered bytes, including replaced values.
type JarOutput struct {
	forms    map[string]bool
	replacer *strings.Replacer
}

func (o *JarOutput) protect(values ...string) {
	if o == nil {
		return
	}
	if o.forms == nil {
		o.forms = make(map[string]bool)
	}
	for _, v := range values {
		if v == "" || o.forms[v] {
			continue
		}
		o.replacer = nil
		o.forms[v] = true
		for _, form := range secretmatch.EncodedForms(v) {
			if form != "" {
				o.forms[form] = true
			}
		}
	}
}

// Text preserves the received bytes unless they contain a held value.
func (o *JarOutput) Text(s string) string {
	if o == nil || len(o.forms) == 0 || s == "" {
		return s
	}
	if o.replacer == nil {
		forms := make([]string, 0, len(o.forms))
		for form := range o.forms {
			forms = append(forms, form)
		}
		sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
		pairs := make([]string, 0, len(forms)*2)
		for _, form := range forms {
			pairs = append(pairs, form, secretmatch.RedactedMarker)
		}
		o.replacer = strings.NewReplacer(pairs...)
	}
	return o.replacer.Replace(s)
}

type jarDiagnostic struct {
	cause  error
	text   string
	reject *toolrejection.ToolReject
}

func (e *jarDiagnostic) Error() string { return e.text }
func (e *jarDiagnostic) Unwrap() error { return e.cause }
func (e *jarDiagnostic) As(target any) bool {
	p, ok := target.(**toolrejection.ToolReject)
	if !ok || e.reject == nil {
		return false
	}
	*p = e.reject
	return true
}

func (o *JarOutput) diagnostic(err error) error {
	if err == nil || o == nil || len(o.forms) == 0 {
		return err
	}
	projected := &jarDiagnostic{cause: err, text: o.Text(err.Error())}
	if source := toolrejection.AsToolReject(err); source != nil {
		reject := *source
		reject.Data, _ = o.fact(source.Data).(map[string]any)
		projected.reject = &reject
	}
	return projected
}

func (o *JarOutput) fact(raw any) any {
	switch value := raw.(type) {
	case string:
		return o.Text(value)
	case []string:
		clean := make([]string, len(value))
		for i, s := range value {
			clean[i] = o.Text(s)
		}
		return clean
	case map[string]string:
		clean := make(map[string]string, len(value))
		for k, s := range value {
			clean[k] = o.Text(s)
		}
		return clean
	case []any:
		clean := make([]any, len(value))
		for i, v := range value {
			clean[i] = o.fact(v)
		}
		return clean
	case map[string]any:
		clean := make(map[string]any, len(value))
		for k, v := range value {
			clean[k] = o.fact(v)
		}
		return clean
	default:
		return value
	}
}
