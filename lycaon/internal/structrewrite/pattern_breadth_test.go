package structrewrite

import "testing"

func TestBareBinaryMetavarOpPythonOr(t *testing.T) {
	op, ok := BareBinaryMetavarOp(t.Context(), "python", "app.py", "$T | $B")
	if !ok {
		t.Fatal("expected $T | $B to be a bare binary metavar op")
	}
	if op != "|" {
		t.Fatalf("op = %q want |", op)
	}
}

func TestBareBinaryMetavarOpNarrowUnion(t *testing.T) {
	if _, ok := BareBinaryMetavarOp(t.Context(), "python", "app.py", "$T | None"); ok {
		t.Fatal("$T | None must not be treated as bare")
	}
}

func TestBareBinaryMetavarOpCallPattern(t *testing.T) {
	if _, ok := BareBinaryMetavarOp(t.Context(), "go", "main.go", "fmt.Println($A)"); ok {
		t.Fatal("call pattern must not be treated as bare")
	}
}

func TestBareBinaryMetavarOpGoAdd(t *testing.T) {
	op, ok := BareBinaryMetavarOp(t.Context(), "go", "main.go", "$A + $B")
	if !ok {
		t.Fatal("expected $A + $B to be a bare binary metavar op")
	}
	if op != "+" {
		t.Fatalf("op = %q want +", op)
	}
}
