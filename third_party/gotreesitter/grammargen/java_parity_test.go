package grammargen

import "testing"

func TestJavaPrimitiveAndRequiresModifierDeepParity(t *testing.T) {
	assertImportedDeepParityCases(t, "java", []struct {
		name string
		src  string
	}{
		{
			name: "primitive_types",
			src:  "class A { int x; double y; }",
		},
		{
			name: "requires_transitive_modifier",
			src:  "module M { requires transitive com.example.network; }",
		},
		{
			name: "requires_static_modifier",
			src:  "module M { requires static com.example.network; }",
		},
		{
			name: "static_import",
			src:  "import static java.util.Collections.emptyList; class A {}",
		},
		{
			name: "repeated_modifiers_and_void_type",
			src:  "class A { public static void f() {} static final int x = 1; }",
		},
		{
			name: "static_initializer",
			src:  "class A { static {} }",
		},
	})
}
