package summarize

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

// Strict decoding refuses an unknown key; this refuses the opposite drift, a
// cap the shipped file omits, which would decode as zero.
func TestShippedCapsSetEveryField(t *testing.T) {
	raw, err := config.Read(config.Summarize)
	if err != nil {
		t.Fatalf("read shipped summarize caps: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse shipped summarize caps: %v", err)
	}
	checked := 0
	var walk func(typ reflect.Type, node map[string]any, at string)
	walk = func(typ reflect.Type, node map[string]any, at string) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			key := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if key == "" || key == "-" {
				continue
			}
			value, ok := node[key]
			if !ok {
				t.Errorf("shipped summarize caps omit %s%s", at, key)
				continue
			}
			if field.Type.Kind() == reflect.Struct {
				child, ok := value.(map[string]any)
				if !ok {
					t.Errorf("shipped summarize caps %s%s is not a mapping", at, key)
					continue
				}
				walk(field.Type, child, at+key+".")
				continue
			}
			checked++
		}
	}
	walk(reflect.TypeOf(Caps{}), doc, "")
	if checked == 0 {
		t.Fatal("no summarize cap fields checked")
	}
}
