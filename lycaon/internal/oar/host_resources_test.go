package oar

import "testing"

func TestHostResourceObservationsUseOARZeroValues(t *testing.T) {
	t.Parallel()

	if got := EvalHostResourceStatus(nil, "missing"); got != "" {
		t.Fatalf("nil status = %q, want OAR string zero value", got)
	}
	if got := EvalHostResourcePolicy(nil, "missing"); got != "" {
		t.Fatalf("nil policy = %q, want OAR string zero value", got)
	}

	gc := &GuardContext{
		HostResourceStatus: map[string]string{"docker": "available"},
		HostResourcePolicy: map[string]string{"docker": "ask"},
	}
	if got := EvalHostResourceStatus(gc, "docker"); got != "available" {
		t.Fatalf("known status = %q, want available", got)
	}
	if got := EvalHostResourcePolicy(gc, "docker"); got != "ask" {
		t.Fatalf("known policy = %q, want ask", got)
	}
	if got := EvalHostResourceStatus(gc, "missing"); got != "" {
		t.Fatalf("missing status = %q, want OAR string zero value", got)
	}
	if got := EvalHostResourcePolicy(gc, "missing"); got != "" {
		t.Fatalf("missing policy = %q, want OAR string zero value", got)
	}
}
