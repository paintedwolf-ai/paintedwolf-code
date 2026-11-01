package jq

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
)

func TestJSONSizeMatchesCompactEncodingAtEveryBoundary(t *testing.T) {
	object, err := NewValueStream(strings.NewReader(`{"a":[1,null,"two"],"b":{}}`)).Decode()
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	values := []any{nil, true, false, 17, -1.5e20, math.Inf(1), big.NewInt(123456789), json.Number("1.20e+03"), "\b\f\n\r\t\x00\x7f\"\\<>&é😀\u2028\xff", []any{}, object}
	for _, value := range values {
		encoded, err := EncodeJSON(value, false)
		if err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
		for _, maximum := range []int64{0, int64(len(encoded) - 1), int64(len(encoded)), int64(len(encoded) + 1)} {
			size, err := JSONSize(t.Context(), value, maximum)
			if err != nil || size != min(int64(len(encoded)), maximum+1) {
				t.Fatalf("%T cap %d: size %d want %d error %v", value, maximum, size, len(encoded), err)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := JSONSize(ctx, object, 1000); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := JSONSize(t.Context(), make(chan int), 1000); err == nil {
		t.Fatal("unsupported value accepted")
	}
}
