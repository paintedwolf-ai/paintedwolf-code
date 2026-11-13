package evidence

import (
	"sync"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SourceDeliveryKind is the kind of every source window a program delivers.
const SourceDeliveryKind = "read"

var nativeOperations = sync.OnceValues(func() (map[string]bool, error) {
	cfg, err := nativemanifest.Load()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, name := range cfg.AllOperations() {
		out[name] = true
	}
	return out, nil
})

// ReceiptKind is the kind a call to tool is recorded under. A tool with a
// command form runs as that program, so its receipt is the command's.
func (b *Binding) ReceiptKind(tool string) string {
	if toolcontract.CommandFormTool(tool) {
		return b.ToolKind("command")
	}
	return b.ToolKind(tool)
}

// ReceiptKinds maps every declared tool to ReceiptKind.
func (b *Binding) ReceiptKinds() map[string]string {
	out := map[string]string{}
	for _, tool := range b.DeclaredToolNames() {
		out[tool] = b.ReceiptKind(tool)
	}
	return out
}

// MintedKinds maps every evidence kind to whether the reachable tools mint it,
// directly or through a native operation command reaches. A reachable tool with
// a command form mints its program's receipt and delivered source windows.
func (b *Binding) MintedKinds(reachable map[string]bool) (map[string]bool, error) {
	operations, err := nativeOperations()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for kind := range b.KindShapeMap() {
		out[kind] = false
	}
	for _, tool := range b.DeclaredToolNames() {
		if !reachable[tool] && !(operations[tool] && toolcontract.CommandLanguageReachable(tool, reachable)) {
			continue
		}
		for _, kind := range b.ProducibleKinds(tool) {
			out[kind] = true
		}
		if reachable[tool] && toolcontract.CommandFormTool(tool) {
			out[b.ReceiptKind(tool)] = true
			out[SourceDeliveryKind] = true
		}
	}
	return out, nil
}
