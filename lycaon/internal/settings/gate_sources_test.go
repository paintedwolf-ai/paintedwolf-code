package settings

import "github.com/lycaon/lycaon/internal/detectionpack"

func emptySources() Sources {
	return Sources{Detections: func() DetectionSource { return detectionpack.NewGateSource(nil) }}
}
