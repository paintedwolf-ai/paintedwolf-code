package settingstest

import (
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil/gatefixture"
)

func EmptySources() settings.Sources {
	return settings.Sources{Detections: func() settings.DetectionSource { return gatefixture.EmptyDetections{} }}
}
