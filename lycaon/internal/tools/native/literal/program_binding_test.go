package literal

import (
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestProgramBindingsShareAccountLookupAndKeepSeparateMemory(t *testing.T) {
	shared := &Owners{accounts: &accounts{}}
	firstMemory, secondMemory := &toolcontract.ProgramMemory{}, &toolcontract.ProgramMemory{}
	first, second := shared.ForProgram(firstMemory), shared.ForProgram(secondMemory)
	if shared.Revisions.Memory != nil || first.Revisions.Memory != firstMemory || second.Revisions.Memory != secondMemory {
		t.Fatal("program memory escaped its binding")
	}
	lookups := 0
	var calls sync.WaitGroup
	for i := 0; i < 32; i++ {
		bound := first
		if i%2 == 0 {
			bound = second
		}
		calls.Go(func() {
			if got := bound.accounts.name(&bound.accounts.users, 42, false, func(string) (string, error) { lookups++; return "fixture account", nil }); got != "fixture account" {
				t.Errorf("account lookup = %q", got)
			}
		})
	}
	calls.Wait()
	if lookups != 1 {
		t.Fatalf("shared cache performed %d lookups", lookups)
	}
}
