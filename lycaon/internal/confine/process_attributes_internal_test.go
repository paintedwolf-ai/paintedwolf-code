package confine

import (
	"math"
	"reflect"
	"runtime"
	"testing"
)

func TestProcessAttributeArgumentsRoundTrip(t *testing.T) {
	attributes := ProcessAttributes{Limits: []ResourceLimit{{Resource: "nofile", Soft: 256, Hard: 1024}, {Resource: "core", Soft: 0, Hard: math.MaxUint64}}, IgnoreHangup: true}
	args := attributes.arguments()
	if runtime.GOOS == "windows" {
		if args != nil {
			t.Fatalf("windows helper arguments %v", args)
		}
		return
	}
	want := []string{"--resource-limit", "nofile=256:1024", "--resource-limit", "core=0:18446744073709551615", "--ignore-hangup"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("arguments %v, want %v", args, want)
	}
	for i, limit := range attributes.Limits {
		parsed, err := parseResourceLimit(args[2*i+1])
		if err != nil || parsed != limit {
			t.Fatalf("parse %q = %+v, %v", args[2*i+1], parsed, err)
		}
	}
	if (ProcessAttributes{}).arguments() != nil {
		t.Fatal("empty attributes rendered helper flags")
	}
}

func TestParseResourceLimitRefusesMalformedValues(t *testing.T) {
	for _, value := range []string{"", "nofile", "nofile=1", "rss=1:2", "nofile=x:2", "nofile=1:y", "nofile=3:2", "nofile=-1:2"} {
		if limit, err := parseResourceLimit(value); err == nil {
			t.Errorf("parseResourceLimit(%q) = %+v; want refused", value, limit)
		}
	}
}
