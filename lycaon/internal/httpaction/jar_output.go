package httpaction

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
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
