package toolkit

import "testing"

func TestBoundedIntArgClamp(t *testing.T) {
	arg := BoundedIntArg(map[string]any{"max_results": float64(99999)}, "max_results", 500, 1, 500)
	if arg.Effective != 500 || !arg.Clamped || arg.Requested == nil || *arg.Requested != 99999 {
		t.Fatalf("arg = %+v", arg)
	}
}

func TestBoundedIntArgDefaultNoClamp(t *testing.T) {
	arg := BoundedIntArg(map[string]any{}, "max_results", 500, 1, 500)
	if arg.Effective != 500 || arg.Clamped || arg.Requested != nil {
		t.Fatalf("arg = %+v", arg)
	}
}

func TestBoundedIntArgMinClamp(t *testing.T) {
	arg := BoundedIntArg(map[string]any{"max_depth": float64(0)}, "max_depth", 8, 1, 8)
	if arg.Effective != 1 || !arg.Clamped {
		t.Fatalf("arg = %+v", arg)
	}
}
