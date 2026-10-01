package events

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectScopesUseUUIDs(t *testing.T) {
	for _, topic := range api.AllEventTopicValues() {
		t.Run(string(topic), func(t *testing.T) {
			for _, project := range []string{"invalid", "/tmp/project", "1234", "00000000-0000-0000-0000-00000000000z"} {
				if err := ValidatePublishScope(topic, PublishKey{Project: project}); err == nil {
					t.Errorf("accepted invalid project scope %q", project)
				}
			}
			if err := ValidatePublishScope(topic, PublishKey{Project: "018eca20-5608-7a55-824f-920998f472b2"}); err != nil {
				t.Fatalf("valid project scope: %v", err)
			}
		})
	}
}
