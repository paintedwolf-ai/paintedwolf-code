package viewstate

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func folder(client string, focused bool) api.ReplaceProjectViewRequest {
	return api.ReplaceProjectViewRequest{ClientID: client, Focused: focused, Folder: &api.ViewReportPath{RootID: "r", Path: client}}
}

func clients(reports []Report) []string {
	out := make([]string, len(reports))
	for i, r := range reports {
		out[i] = r.ClientID
	}
	return out
}

func TestVisibleOrdersWindowsByLastFocus(t *testing.T) {
	c := &clock{at: time.Unix(1000, 0)}
	s := New(c.now)
	testutil.FailErr(t, "report a", s.Replace("p", "alex", folder("a", true)))
	c.at = c.at.Add(time.Second)
	testutil.FailErr(t, "report b", s.Replace("p", "alex", folder("b", true)))
	c.at = c.at.Add(time.Second)
	testutil.FailErr(t, "renew a unfocused", s.Replace("p", "alex", folder("a", false)))
	if got := clients(s.Visible("p", "alex")); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("windows = %v; want the last focused first", got)
	}
}

func TestReportsExpireWithoutRenewal(t *testing.T) {
	c := &clock{at: time.Unix(1000, 0)}
	s := New(c.now)
	testutil.FailErr(t, "report", s.Replace("p", "alex", folder("a", true)))
	c.at = c.at.Add(TTL - time.Nanosecond)
	if len(s.Visible("p", "alex")) != 1 {
		t.Fatal("report expired before its time")
	}
	c.at = c.at.Add(time.Nanosecond)
	if got := s.Visible("p", "alex"); len(got) != 0 {
		t.Fatalf("expired report still visible: %v", clients(got))
	}
}

func TestEmptyReportWithdrawsTheWindow(t *testing.T) {
	s := New(nil)
	testutil.FailErr(t, "report", s.Replace("p", "alex", folder("a", true)))
	testutil.FailErr(t, "withdraw", s.Replace("p", "alex", api.ReplaceProjectViewRequest{ClientID: "a", Focused: true}))
	if got := s.Visible("p", "alex"); len(got) != 0 {
		t.Fatalf("empty report kept the window: %v", clients(got))
	}
}

func TestReportsStayWithTheirPersonAndProject(t *testing.T) {
	s := New(nil)
	testutil.FailErr(t, "report", s.Replace("p", "alex", folder("a", true)))
	if err := s.Replace("p", "sam", folder("a", true)); !errors.Is(err, ErrClientIdentity) {
		t.Fatalf("another person reused a window: %v", err)
	}
	testutil.FailErr(t, "report other window", s.Replace("p", "sam", folder("b", true)))
	if got := clients(s.Visible("p", "alex")); len(got) != 1 || got[0] != "a" {
		t.Fatalf("alex sees %v", got)
	}
	if got := s.Visible("q", "alex"); len(got) != 0 {
		t.Fatalf("another project sees %v", clients(got))
	}
}

func TestVisibleReadsAtMostMaxWindows(t *testing.T) {
	c := &clock{at: time.Unix(1000, 0)}
	s := New(c.now)
	for i := range MaxWindows + 2 {
		c.at = c.at.Add(time.Second)
		testutil.FailErr(t, "report", s.Replace("p", "alex", folder(string(rune('a'+i)), true)))
	}
	got := s.Visible("p", "alex")
	if len(got) != MaxWindows || got[0].ClientID != string(rune('a'+MaxWindows+1)) {
		t.Fatalf("windows = %v; want the %d most recently focused", clients(got), MaxWindows)
	}
}
