package backgroundwork

import (
	"testing"
	"time"
)

func TestSharedQueuedWorkTracksConsumerPriority(t *testing.T) {
	group := &PriorityGroup{}
	leaveBackground := group.Add(PriorityProactive)
	defer leaveBackground()
	now := time.Now()
	queued := waiter{req: Request{Priority: PriorityProactive, Interests: group}, queued: now}
	if queued.effectivePriority(now) != PriorityProactive {
		t.Fatal("background work claimed interactive admission")
	}
	leaveVisible := group.Add(PriorityInteractive)
	if queued.effectivePriority(now) != PriorityInteractive {
		t.Fatal("visible consumer did not promote queued shared work")
	}
	leaveVisible()
	leaveVisible()
	if queued.effectivePriority(now) != PriorityProactive {
		t.Fatal("departed consumer left priority elevated")
	}
	if queued.effectivePriority(now.Add(2*AgingInterval)) != PriorityInteractive {
		t.Fatal("shared work lost aging")
	}
}
