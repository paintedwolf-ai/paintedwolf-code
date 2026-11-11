package nativegen

import (
	"errors"
	"math"

	"github.com/lycaon/lycaon/internal/inference"
)

type helloResult struct {
	Kind            string                   `json:"kind"`
	QuiescentMemory bool                     `json:"quiescent_memory"`
	Protocol        uint32                   `json:"protocol"`
	Backend         string                   `json:"backend"`
	ArtifactID      string                   `json:"artifact_id"`
	PhysicalDevice  inference.PhysicalDevice `json:"physical_device"`
}

type resourcePlan struct {
	ResidentWeightsBytes   uint64 `json:"resident_weights_bytes"`
	MappedPLEBytes         uint64 `json:"mapped_ple_bytes"`
	KVCapacityBytes        uint64 `json:"kv_capacity_bytes"`
	WorkspaceBytes         uint64 `json:"workspace_bytes"`
	AllocatorCacheBytes    uint64 `json:"allocator_cache_bytes"`
	DeviceReservationBytes uint64 `json:"device_reservation_bytes"`
	HostReservationBytes   uint64 `json:"host_reservation_bytes"`
}

type resourceResult struct {
	Kind string       `json:"kind"`
	Plan resourcePlan `json:"plan"`
}

func (p resourcePlan) validate() error {
	if p.ResidentWeightsBytes == 0 || p.MappedPLEBytes == 0 || p.KVCapacityBytes == 0 || p.WorkspaceBytes == 0 || p.HostReservationBytes == 0 {
		return errors.New("native generation: incomplete memory plan")
	}
	var total uint64
	for _, bytes := range []uint64{p.ResidentWeightsBytes, p.KVCapacityBytes, p.WorkspaceBytes, p.AllocatorCacheBytes} {
		if bytes > math.MaxUint64-total {
			return errors.New("native generation: memory plan overflow")
		}
		total += bytes
	}
	if total != p.DeviceReservationBytes || p.HostReservationBytes > math.MaxUint64-total {
		return errors.New("native generation: inconsistent memory plan")
	}
	return nil
}

type samplingDefaults struct {
	Temperature float32 `json:"temperature"`
	TopP        float32 `json:"top_p"`
	TopK        uint32  `json:"top_k"`
}

type loadedResult struct {
	Kind          string           `json:"kind"`
	ContextTokens int              `json:"context_tokens"`
	PrefillChunk  int              `json:"prefill_chunk"`
	Sampling      samplingDefaults `json:"sampling"`
}

type renderedResult struct {
	Kind             string   `json:"kind"`
	PromptTokens     []uint32 `json:"prompt_tokens"`
	CheckpointTokens int      `json:"checkpoint_tokens"`
}

type begunResult struct {
	Kind               string `json:"kind"`
	RequestID          uint64 `json:"request_id"`
	ReusedPrefixTokens int    `json:"reused_prefix_tokens"`
}

type progressResult struct {
	expected *progressCursor
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Progress struct {
		RequestID             uint64  `json:"request_id"`
		ProcessedPromptTokens int     `json:"processed_prompt_tokens"`
		GeneratedTokens       int     `json:"generated_tokens"`
		ReusedPrefixTokens    int     `json:"reused_prefix_tokens"`
		Token                 *uint32 `json:"token"`
		StopReason            *string `json:"stop_reason"`
	} `json:"progress"`
}
