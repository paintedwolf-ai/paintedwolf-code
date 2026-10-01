//go:build scanstress

package scanstress

import "fmt"

func fmtSscan(s string, n *int) (int, error) { return fmt.Sscanf(s, "%d", n) }
