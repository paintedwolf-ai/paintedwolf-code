//go:build !linux

package systemproxy

import "context"

// CurrentContext checks cancellation before the platform's bounded native read.
func CurrentContext(ctx context.Context) (Configuration, error) {
	if err := ctx.Err(); err != nil {
		return Configuration{}, err
	}
	return Current()
}
