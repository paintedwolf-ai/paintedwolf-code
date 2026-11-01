package nativegen

import (
	"encoding/json"
	"os"
)

func runtimeFixture() {
	artifact := ""
	for i, argument := range os.Args {
		if argument == "--artifact-id" && i+1 < len(os.Args) {
			artifact = os.Args[i+1]
		}
	}
	var active beginRequest
	generated := 0
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
			result = map[string]any{"kind": "loaded", "context_tokens": request.Command.ContextTokens, "prefill_chunk": request.Command.PrefillChunk, "sampling": samplingDefaults{Temperature: 0, TopP: 1}}
		case "render":
			result = map[string]any{"kind": "rendered", "prompt_tokens": []uint32{2, 7, 8}, "checkpoint_tokens": 1}
		case "begin":
			raw, err := json.Marshal(request.Command.Request)
			if err != nil || json.Unmarshal(raw, &active) != nil {
				os.Exit(3)
			}
			generated = 0
			result = map[string]any{"kind": "begun", "request_id": active.RequestID, "reused_prefix_tokens": 0}
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
			processed := 0
			if generated > 0 {
				processed = len(active.PromptTokens)
			}
			result = map[string]any{"kind": "progress", "text": text, "progress": map[string]any{"request_id": active.RequestID, "processed_prompt_tokens": processed, "generated_tokens": generated, "reused_prefix_tokens": 0, "token": token, "stop_reason": stop}}
		default:
			os.Exit(4)
		}
		payload, err = json.Marshal(map[string]any{"status": "ok", "id": request.ID, "result": result})
		if err != nil || writeProtocolFrame(os.Stdout, payload) != nil {
			return
		}
	}
}
