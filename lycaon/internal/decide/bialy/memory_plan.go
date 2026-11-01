package bialy

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// MemoryPlan includes the resident backbone, all heads, full-length batches, and retained buffers.
type MemoryPlan struct {
	HostBytes   uint64
	DeviceBytes uint64
	MaxRows     int
}

// PlanMemory admits the bundled ModernBERT architecture without loading tensors.
func PlanMemory(c Config, backend string, rows int) (MemoryPlan, error) {
	if rows != 2 && rows != 8 {
		return MemoryPlan{}, errors.New("decide: unsupported managed batch size")
	}
	var encoder struct {
		ModelType    string `json:"model_type"`
		Hidden       int    `json:"hidden_size"`
		Layers       int    `json:"num_hidden_layers"`
		Heads        int    `json:"num_attention_heads"`
		Intermediate int    `json:"intermediate_size"`
		Vocabulary   int    `json:"vocab_size"`
	}
	if err := readPlanConfig(filepath.Join(c.ModelDir, "encoder", "config.json"), &encoder); err != nil {
		return MemoryPlan{}, err
	}
	if encoder.ModelType != "modernbert" || encoder.Hidden != 768 || encoder.Layers != 22 || encoder.Heads != 12 || encoder.Intermediate != 1152 || encoder.Vocabulary != 256000 {
		return MemoryPlan{}, errors.New("decide: checkpoint architecture has no managed memory plan")
	}
	var agent struct {
		Length     int `json:"max_len"`
		HeadLayers int `json:"head_layers"`
	}
	if err := readPlanConfig(filepath.Join(c.ModelDir, "rl_agent_config.json"), &agent); err != nil {
		return MemoryPlan{}, err
	}
	if agent.Length < 1 || agent.Length > 1024 || agent.HeadLayers != 2 || c.HeadMaxLen < 0 || c.HeadMaxLen > 1024 {
		return MemoryPlan{}, errors.New("decide: checkpoint execution limits have no managed memory plan")
	}
	weights, err := planFileBytes(filepath.Join(c.ModelDir, "model.safetensors"), 768<<20)
	if err != nil {
		return MemoryPlan{}, err
	}
	if len(c.Heads) > 6 {
		return MemoryPlan{}, errors.New("decide: too many resident heads")
	}
	var heads uint64
	for _, path := range c.Heads {
		bytes, err := planFileBytes(path, 64<<20)
		if err != nil {
			return MemoryPlan{}, err
		}
		heads += bytes
	}
	plan := MemoryPlan{MaxRows: rows}
	switch backend {
	case "mlx":
		plan.HostBytes = 384 << 20
		plan.DeviceBytes = weights + heads + (768 << 20)
		if rows == 8 {
			plan.DeviceBytes += 1536 << 20
		}
	case "vulkan":
		plan.HostBytes = 1 << 30
		plan.DeviceBytes = weights + heads + (2304 << 20)
	case "cpu":
		plan.HostBytes = 2*(weights+heads) + (1536 << 20)
	default:
		return MemoryPlan{}, errors.New("decide: unsupported managed backend")
	}
	return plan, nil
}

func readPlanConfig(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errors.New("decide: checkpoint planning metadata exceeds its bound")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("decide: invalid checkpoint planning metadata")
	}
	return nil
}

func planFileBytes(path string, maximum int64) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return 0, errors.New("decide: checkpoint file exceeds its managed memory bound")
	}
	return uint64(info.Size()), nil
}
