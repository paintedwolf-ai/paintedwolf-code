//go:build stress

package sourcecatalog

import (
	"fmt"
	"testing"
)

func TestStressTreeWidePageAndEditWork(t *testing.T) {
	for _, size := range []int{100_000, 1_000_000, 10_000_000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			assertTreeWidePageAndEditWork(t, size)
		})
	}
}
