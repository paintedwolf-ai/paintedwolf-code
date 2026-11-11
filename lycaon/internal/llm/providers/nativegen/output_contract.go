package nativegen

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

const protocolVersion = 2

type outputContract struct {
	AllowedTools []string `json:"allowed_tools"`
}

func requestOutputContract(req modelcall.CompletionRequest) (outputContract, error) {
	contract := outputContract{AllowedTools: []string{}}
	if !req.ToolsCallable() || req.ResponseFormat != nil {
		return contract, nil
	}
	seen := make(map[string]bool)
	total := 0
	for _, tool := range req.Tools {
		name := tool.Name
		total += len(name)
		if name == "" || len(name) > 256 || !utf8.ValidString(name) || strings.TrimSpace(name) != name || strings.ContainsAny(name, "{\r\n") || seen[name] || total > 64<<10 || len(contract.AllowedTools) == 512 {
			return outputContract{}, errors.New("native generation: invalid offered tool names")
		}
		seen[name] = true
		contract.AllowedTools = append(contract.AllowedTools, name)
	}
	return contract, nil
}
