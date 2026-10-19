//go:build !linux

package configlayout

import "context"

// RetainRunningNix has no roots to register on other operating systems.
func RetainRunningNix(context.Context) (*NixRetention, error) { return &NixRetention{}, nil }
