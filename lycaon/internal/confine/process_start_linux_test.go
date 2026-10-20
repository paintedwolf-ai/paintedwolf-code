//go:build linux

package confine

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestLinuxNativeArgumentBudgetUsesInheritedStackAndPageFloor(t *testing.T) {
	for _, c := range []struct {
		stack      uint64
		page, want int
	}{{8 << 20, 4096, 2 << 20}, {1 << 20, 4096, 256 << 10}, {0, 4096, 128 << 10}, {^uint64(0), 4096, 6 << 20}, {0, 65536, 2 << 20}} {
		if got := linuxArgumentBudget(c.stack, c.page); got != c.want {
			t.Fatalf("native vector allowance got%d want%d", got, c.want)
		}
	}
	var stack unix.Rlimit
	testutil.FailErr(t, "observe inherited stack bound", unix.Getrlimit(unix.RLIMIT_STACK, &stack))
	got, err := nativeArgumentBudget()
	testutil.FailErr(t, "derive native vector allowance", err)
	if got != linuxArgumentBudget(stack.Cur, os.Getpagesize()) {
		t.Fatal("native allowance does not match inherited kernel facts")
	}
}
