package maintainability

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func measuredShape(t *testing.T, body string) string {
	t.Helper()
	src := source{Path: "pkg/service.go", Language: "go", Body: []byte(body)}
	inv := &inventory{measured: newMeasurements(), sources: map[string][]string{}, declarations: map[string][]span{}, methodSpans: map[string][]span{}, methods: map[string]map[string]bool{}}
	_, err := inv.measureGo(src)
	testutil.FailErr(t, "measure shape fixture", err)
	shape, err := typeShape(&workingTree{bodies: map[string][]byte{src.Path: src.Body}}, inv, "pkg/p.Service")
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
