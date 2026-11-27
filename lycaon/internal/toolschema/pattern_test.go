package toolschema

import "testing"

func TestSchemaPatternAdmitsOnlyValuesItCanCheck(t *testing.T) {
	schema := func(pattern string) map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string", "pattern": pattern},
		}}
	}
	for _, tc := range []struct {
		pattern, value string
		want           bool
	}{
		{"^[a-z]+$", "abc", true},
		{"^[a-z]+$", "ABC", false},
		{"^[a-z]+$", "abc", true},
		{"^(?=a)a$", "a", false},
	} {
		if got := SchemaAdmitsStringValue(schema(tc.pattern), "name", tc.value); got != tc.want {
			t.Errorf("pattern %q value %q admitted = %t, want %t", tc.pattern, tc.value, got, tc.want)
		}
	}
	if re, cached := patternCache.Load("^[a-z]+$"); !cached || re == nil {
		t.Fatal("compiled pattern was not cached")
	}
	if re, cached := patternCache.Load("^(?=a)a$"); !cached || re != nil {
		t.Fatal("an uncompilable pattern was not recorded as unevaluable")
	}
}
