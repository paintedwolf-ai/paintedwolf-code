package toolhost

import "github.com/lycaon/lycaon/internal/settings"

// SetDetectionSource binds the live catalog reader during startup.
func (r *Runtime) SetDetectionSource(source func() settings.DetectionSource) {
	if r != nil && r.gateBuilder != nil {
		r.gateBuilder.WithDetections(source)
	}
}
