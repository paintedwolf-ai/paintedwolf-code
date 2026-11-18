package settings

import "github.com/lycaon/lycaon/internal/testutil/gatefixture"

func emptySources() Sources {
	return Sources{
		Detections:    func() DetectionSource { return gatefixture.EmptyDetections{} },
		Pins:          inertPins{},
		ApprovalRules: inertApprovalRules{},
	}
}
