package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestBedrockUsagePreservesWriteLifetimeAndPresence(t *testing.T) {
	u := bedrockUsage(&brtypes.TokenUsage{InputTokens: aws.Int32(100), OutputTokens: aws.Int32(20),
		CacheReadInputTokens: aws.Int32(400), CacheWriteInputTokens: aws.Int32(300),
		CacheDetails: []brtypes.CacheDetail{{InputTokens: aws.Int32(100), Ttl: brtypes.CacheTTLOneHour},
			{InputTokens: aws.Int32(200), Ttl: brtypes.CacheTTLFiveMinutes}}})
	if u.PromptTokens != 800 || u.CacheCreationInputTokens != 300 || u.CacheCreation1HInputTokens != 100 || !u.Reported() || u.Incomplete {
		t.Fatalf("Bedrock usage = %+v", u)
	}
	if bedrockUsage(nil).Reported() || !bedrockUsage(&brtypes.TokenUsage{InputTokens: aws.Int32(0)}).Incomplete {
		t.Fatal("missing counters became complete usage")
	}
}
