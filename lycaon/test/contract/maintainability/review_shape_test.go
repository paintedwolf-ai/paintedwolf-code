package maintainability

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func measuredShape(t *testing.T, body string) string {
	t.Helper()
	return measuredFileShapes(t, map[string]string{"pkg/service.go": body})
}

func measuredFileShapes(t *testing.T, bodies map[string]string) string {
	t.Helper()
	inv := &inventory{measured: newMeasurements(), sources: map[string][]string{}, declarations: map[string][]span{}, methodSpans: map[string][]span{}, methods: map[string]map[string]bool{}}
	tree := &workingTree{bodies: map[string][]byte{}}
	for path, body := range bodies {
		src := source{Path: path, Language: "go", Body: []byte(body)}
		_, err := inv.measureGo(src)
		testutil.FailErr(t, "measure shape fixture", err)
		tree.bodies[path] = src.Body
	}
	shape, err := typeShape(tree, inv, "pkg/p.Service")
	testutil.FailErr(t, "fingerprint shape", err)
	return shape
}

func TestTypeShapeIgnoresImplementationButDetectsContractChanges(t *testing.T) {
	base := "package p; type Service struct { Value string }; func (s *Service) Read() string { return s.Value }"
	shape := measuredShape(t, base)
	for _, body := range []string{
		"package p\n// Service reads values.\ntype Service struct { Value string }\nfunc (s *Service) Read() string { return \"different\" }",
		"package p; func (s *Service) Read() string { return s.Value }; type Service struct { Value string }",
	} {
		if measuredShape(t, body) != shape {
			t.Fatal("implementation or declaration ordering renewed shape review")
		}
	}
	for _, body := range []string{
		"package p; type Service struct { Value string }; func (s Service) Read() string { return s.Value }",
		"package p; type Service[T any] struct { Value string }; func (s *Service[T]) Read() string { return s.Value }",
		"package p; type Service struct { Other string }; func (s *Service) Read() string { return s.Other }",
		"package p; type Service struct { Value int }; func (s *Service) Read() string { return \"\" }",
		"package p; type Service struct { Value string }; func (s *Service) Read() (string, error) { return s.Value, nil }",
		"package p; type Service struct { Value string }; func (s *Service) Read() string { return s.Value }; func (s *Service) Clear() {}",
	} {
		if measuredShape(t, body) == shape {
			t.Fatal("changed type contract did not renew shape review")
		}
	}
}

func TestTypeShapeRejectsMissingAndInvalidSource(t *testing.T) {
	inv := &inventory{sources: map[string][]string{"pkg/p.Service": {"pkg/service.go"}}}
	for _, tree := range []*workingTree{
		{bodies: map[string][]byte{}},
		{bodies: map[string][]byte{"pkg/service.go": []byte("invalid")}},
		{bodies: map[string][]byte{"pkg/service.go": []byte("package p")}},
	} {
		if _, err := typeShape(tree, inv, "pkg/p.Service"); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if _, err := typeShape(&workingTree{}, inv, "bad"); err == nil {
		t.Fatal("invalid identity accepted")
	}
}

func TestTypeShapeTracksImportIdentityRatherThanAlias(t *testing.T) {
	base := measuredShape(t, `package p; import dep "example/first"; type Service struct { Value dep.Record }`)
	if got := measuredShape(t, `package p; import other "example/first"; type Service struct { Value other.Record }`); got != base {
		t.Fatal("import alias changed dependency shape")
	}
	if got := measuredShape(t, `package p; import dep "example/second"; type Service struct { Value dep.Record }`); got == base {
		t.Fatal("changed import identity retained dependency shape")
	}
}

func TestTypeShapeTracksUnaliasedImportWithDifferentPackageName(t *testing.T) {
	base := measuredShape(t, `package p; import "example/first/v2"; type Service struct { Value custom.Record }`)
	if got := measuredShape(t, `package p; import "example/second/v2"; type Service struct { Value custom.Record }`); got == base {
		t.Fatal("unaliased dependency identity disappeared from shape")
	}
}

func TestTypeShapeTracksDotImportIdentity(t *testing.T) {
	base := measuredShape(t, `package p; import . "example/first"; type Service struct { Value Record }`)
	if got := measuredShape(t, `package p; import . "example/second"; type Service struct { Value Record }`); got == base {
		t.Fatal("dot import identity disappeared from shape")
	}
}

func TestTypeShapeBindsImportContextToEachDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name, imports, value string
	}{
		{"dot", `. "example/%s"`, "Record"},
		{"unaliased", `"example/%s/v2"`, "custom.Record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declaration := func(dependency string) string {
				return fmt.Sprintf("package p; import "+tc.imports+"; type Service struct { Value "+tc.value+" }", dependency)
			}
			method := func(dependency string) string {
				return fmt.Sprintf("package p; import "+tc.imports+"; func (*Service) Accept("+tc.value+") {}", dependency)
			}
			base := measuredFileShapes(t, map[string]string{
				"pkg/service.go": declaration("first"), "pkg/method.go": method("second"),
			})
			swapped := measuredFileShapes(t, map[string]string{
				"pkg/service.go": declaration("second"), "pkg/method.go": method("first"),
			})
			if swapped == base {
				t.Fatal("swapped field and parameter dependencies retained the shape")
			}
			moved := measuredFileShapes(t, map[string]string{
				"pkg/method.go": declaration("first"), "pkg/service.go": method("second"),
			})
			if moved != base {
				t.Fatal("moving unchanged declarations renewed shape review")
			}
		})
	}
}
