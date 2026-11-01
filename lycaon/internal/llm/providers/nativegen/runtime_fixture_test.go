package nativegen

import (
	"encoding/json"
	"os"
	"strconv"
)

func runtimeFixture() {
	artifact := ""
	for i, argument := range os.Args {
		if argument == "--artifact-id" && i+1 < len(os.Args) {
			artifact = os.Args[i+1]
		}
	}
	var prefix prefixRequest
	prefixProcessed, chunk := 0, 1
	var active beginRequest
	generated := 0
	measured := 0
	begun := 0
	reused, _ := strconv.Atoi(os.Getenv("PW_NATIVEGEN_REUSED_TOKENS"))
	for {
		payload, err := readProtocolFrame(os.Stdin)
		if err != nil {
			return
		}
		var request struct {
			ID      uint64  `json:"id"`
			Command command `json:"command"`
		}
		if json.Unmarshal(payload, &request) != nil {
			os.Exit(2)
		}
		result := map[string]any{}
		switch request.Command.Method {
		case "hello":
			result = map[string]any{"kind": "hello", "protocol": 1, "artifact_id": artifact, "backend": "vulkan", "physical_device": map[string]string{"kind": "pci", "address": "0000:03:00.0"}}
		case "inspect":
			plan := resourcePlan{ResidentWeightsBytes: 128 << 20, MappedPLEBytes: 2 << 30, KVCapacityBytes: 64 << 20, WorkspaceBytes: 64 << 20, DeviceReservationBytes: 256 << 20, HostReservationBytes: 768 << 20}
			if os.Getenv("PW_NATIVEGEN_SMALL_HOST_PLAN") == "1" {
				plan.HostReservationBytes = 512 << 20
			}
			if os.Getenv("PW_NATIVEGEN_CONTEXT_PLAN") == "1" {
				plan.KVCapacityBytes = uint64(request.Command.ContextTokens) * (16 << 10)
				plan.DeviceReservationBytes = plan.ResidentWeightsBytes + plan.WorkspaceBytes + plan.KVCapacityBytes
			}
			result = map[string]any{"kind": "resources", "plan": plan}
		case "load":
			chunk = request.Command.PrefillChunk
			result = map[string]any{"kind": "loaded", "context_tokens": request.Command.ContextTokens, "prefill_chunk": request.Command.PrefillChunk, "sampling": samplingDefaults{Temperature: 0, TopP: 1}}
		case "measure":
			measured++
			if measured > 1 && os.Getenv("PW_NATIVEGEN_MEASURE_ONCE") == "1" {
				return
			}
			result = map[string]any{"kind": "measured", "input_tokens": 3, "prompt_tokens": []uint32{2, 7, 8}, "checkpoint_tokens": 1}
			if os.Getenv("PW_NATIVEGEN_PREPARATION_BOUNDARIES") == "1" {
				var render renderRequest
				raw, _ := json.Marshal(request.Command.Request)
				if json.Unmarshal(raw, &render) != nil {
					os.Exit(6)
				}
				result["checkpoint_tokens"] = render.CheckpointMessages
			}
		case "render":
			result = map[string]any{"kind": "rendered", "prompt_tokens": []uint32{2, 7, 8}, "checkpoint_tokens": 1}
		case "prepare":
			raw, err := json.Marshal(request.Command.Request)
			if err != nil || json.Unmarshal(raw, &prefix) != nil {
				os.Exit(6)
			}
			expectedKnown := 2
			if os.Getenv("PW_NATIVEGEN_PREPARATION_STANDING_ONLY") == "1" {
				expectedKnown = 1
			}
			if os.Getenv("PW_NATIVEGEN_PREPARATION_BOUNDARIES") == "1" && (prefix.CheckpointTokens != 1 || len(prefix.PromptTokens) != expectedKnown) {
				os.Exit(7)
			}
			prefixProcessed = 0
			result = map[string]any{"kind": "begun", "request_id": prefix.RequestID, "reused_prefix_tokens": 0}
		case "advance_preparation", "cancel_preparation":
			canceled := request.Command.Method == "cancel_preparation"
			if !canceled {
				end := min(prefixProcessed+chunk, len(prefix.PromptTokens))
				if prefixProcessed < prefix.CheckpointTokens {
					end = min(end, prefix.CheckpointTokens)
				}
				prefixProcessed = end
			}
			result = map[string]any{"kind": "preparation_progress", "progress": map[string]any{
				"request_id": prefix.RequestID, "processed_prompt_tokens": prefixProcessed,
				"reused_prefix_tokens": 0, "complete": !canceled && prefixProcessed == len(prefix.PromptTokens), "canceled": canceled,
			}}
		case "begin":
			raw, err := json.Marshal(request.Command.Request)
			if err != nil || json.Unmarshal(raw, &active) != nil {
				os.Exit(3)
			}
			if os.Getenv("PW_NATIVEGEN_CONVERSATION_CACHE") == "1" {
				want := []int{1, 0, 1}
				if begun >= len(want) || active.CheckpointTokens != want[begun] {
					os.Exit(5)
				}
				begun++
			}
			generated = 0
			result = map[string]any{"kind": "begun", "request_id": active.RequestID, "reused_prefix_tokens": reused}
		case "advance", "cancel":
			fault := os.Getenv("PW_NATIVEGEN_RESPONSE_FAULT")
			if request.Command.Method == "advance" && (fault == "before_output" || fault == "after_output" && generated > 0) {
				_, _ = os.Stdout.Write([]byte{0, 0})
				return
			}
			text := ""
			var token *uint32
			var stop *string
			if request.Command.Method == "cancel" {
				value := "canceled"
				stop = &value
			} else {
				generated++
				if generated == 1 && fault != "empty_eos" {
					value := uint32(236812)
					token, text = &value, "4"
					if active.MaxNewTokens == 1 {
						value := "max_tokens"
						stop = &value
					}
				} else {
					value := "eos"
					stop = &value
				}
			}
			processed := reused
			if generated > 0 {
				processed = len(active.PromptTokens)
			}
			result = map[string]any{"kind": "progress", "text": text, "progress": map[string]any{"request_id": active.RequestID, "processed_prompt_tokens": processed, "generated_tokens": generated, "reused_prefix_tokens": reused, "token": token, "stop_reason": stop}}
		default:
			os.Exit(4)
		}
		payload, err = json.Marshal(map[string]any{"status": "ok", "id": request.ID, "result": result})
		if err != nil || writeProtocolFrame(os.Stdout, payload) != nil {
			return
		}
	}
}
