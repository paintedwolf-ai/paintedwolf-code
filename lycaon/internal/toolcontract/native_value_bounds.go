package toolcontract

import (
	"fmt"
	"math"
)

const nativeValueMaxDepth = 1024

// nativeJSONBound covers escaping and the versioned envelope before Marshal allocates.
func nativeJSONBound(value any) (int64, error) {
	size := int64(128)
	switch v := value.(type) {
	case string:
		if int64(len(v)) > (math.MaxInt64-128)/6 {
			return 0, fmt.Errorf("native JSON size overflow")
		}
		size += 6 * int64(len(v))
	case []any:
		for _, item := range v {
			n, err := nativeJSONBound(item)
			if err != nil {
				return 0, err
			}
			if n > math.MaxInt64-size {
				return 0, fmt.Errorf("native JSON size overflow")
			}
			size += n
		}
	}
	if size > math.MaxInt64/3 {
		return 0, fmt.Errorf("native JSON size overflow")
	}
	return size, nil
}
