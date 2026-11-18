package settingstest

import (
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/settings"
)

func EmptySources() settings.Sources {
	return settings.Sources{Detections: func() settings.DetectionSource { return detectionpack.NewGateSource(nil) }}
}
