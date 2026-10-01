package session

import (
	"testing"
)

func TestListenLeaseCovers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		covered, asked []uint16
		want           bool
	}{
		{nil, []uint16{8000}, true},
		{nil, nil, true},
		{[]uint16{8000}, nil, false},
		{[]uint16{8000}, []uint16{8000}, true},
		{[]uint16{8000}, []uint16{9000}, false},
		{[]uint16{8000, 9000}, []uint16{8000, 9000}, true},
	}
	for _, tc := range cases {
		if got := portLeaseCovers(tc.covered, tc.asked); got != tc.want {
			t.Fatalf("covers(%v, %v)=%v want %v", tc.covered, tc.asked, got, tc.want)
		}
	}
}
