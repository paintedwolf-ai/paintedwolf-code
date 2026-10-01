package pagedview

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLeaseSurvivesCapacityPressureAndExpiresAfterRenewalStops(t *testing.T) {
	budget := NewBudget(32)
	registry := NewLeaseRegistry[string](budget, 1, time.Minute)
	defer registry.Close()
	now := time.Now()
	registry.clock = func() time.Time { return now }
	scope := Scope{Person: "person", Project: "project"}
	disposed := 0
	id, err := registry.Put(scope, "held", 16, func(string) { disposed++ })
	testutil.FailErr(t, "retain presentation", err)
	if _, err := registry.Put(scope, "other", 16, nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("capacity replaced an active lease: %v", err)
	}
	other := NewRegistry[string](budget, 10, time.Minute)
	defer other.Close()
	if _, err := other.Put(scope, "large", 32, nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("shared pressure replaced an active lease: %v", err)
	}
	now = now.Add(45 * time.Second)
	registry.Renew(func(value string) bool { return value == "held" })
	now = now.Add(45 * time.Second)
	registry.Sweep()
	value, release, err := registry.Acquire(scope, id)
	testutil.FailErr(t, "read renewed lease", err)
	if value != "held" || disposed != 0 {
		t.Fatalf("lease value=%q disposed=%d", value, disposed)
	}
	registry.Release(scope, id)
	if disposed != 0 {
		t.Fatal("release invalidated an in-flight read")
	}
	release()
	if disposed != 1 || budget.Used() != 0 {
		t.Fatalf("release: disposed=%d bytes=%d", disposed, budget.Used())
	}
	registry.Release(scope, id)
	abandoned, err := registry.Put(scope, "abandoned", 16, nil)
	testutil.FailErr(t, "retain abandoned presentation", err)
	now = now.Add(time.Minute)
	registry.Sweep()
	if _, _, err := registry.Acquire(scope, abandoned); !errors.Is(err, ErrExpired) {
		t.Fatalf("abandoned lease retained: %v", err)
	}
}
